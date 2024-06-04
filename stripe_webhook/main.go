package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	_ "github.com/go-sql-driver/mysql"
)

type StripeWebhookEvent struct {
    Type string     `json:"type"`       // Type of event
    Data StripeData `json:"data"`       // Nested data object
}

// StripeData contains the data object from Stripe webhook JSON
type StripeData struct {
    Object PaymentIntent `json:"object"` // Details of the payment intent
}

// PaymentIntent holds the specific details about the payment intent
type PaymentIntent struct {
    ID          string `json:"id"`          // Transaction ID
    Amount      int64  `json:"amount"`      // Amount in cents
    Currency    string `json:"currency"`    // Currency code, e.g., "usd"
    Description string `json:"description"` // Description of the payment
    Customer    string `json:"customer"`    // Customer ID
}

// Struct to keep the secret key and more params if needed
type AWSParams struct {
	stripeKey string
}

type ConfirmPaymentRequest struct {
    PaymentIntentID string `json:"PaymentIntentID"`
}

// Globals 
var db *sql.DB
var awsParams AWSParams

// getParameter retrieves a parameter from AWS SSM.
func getParameter(region, paramName string) (string, error) {
    sess, err := session.NewSession(&aws.Config{
        Region: aws.String(region),
		CredentialsChainVerboseErrors: aws.Bool(true), // Verbose errors 
    })
    if err != nil {
        log.Printf("Error creating AWS session: %v", err)
        return "", err
    }

    ssmSvc := ssm.New(sess)
    withDecryption := true
    param, err := ssmSvc.GetParameter(&ssm.GetParameterInput{
        Name:           &paramName,
        WithDecryption: &withDecryption,
    })
    if err != nil {
        log.Printf("Error getting parameter '%s': %v", paramName, err)
        return "", err
    }

    return *param.Parameter.Value, nil
}

func initializeDatabase() error {
    sess, err := session.NewSession(&aws.Config{
        Region: aws.String("us-west-2"),
    })
    if err != nil {
        log.Printf("Error creating AWS session: %v", err)
        return err
    }

    ssmSvc := ssm.New(sess)
    paramName := "/application/dev/database/credentials"
    withDecryption := true
    param, err := ssmSvc.GetParameter(&ssm.GetParameterInput{
        Name:           &paramName,
        WithDecryption: &withDecryption,
    })
    if err != nil {
        log.Printf("Error getting parameter: %v", err)
        return err
    }

    var dbCreds struct {
        Username string `json:"username"`
        Password string `json:"password"`
        Host     string `json:"host"`
        Port     int    `json:"port"`
    }
    err = json.Unmarshal([]byte(*param.Parameter.Value), &dbCreds)
    if err != nil {
        log.Printf("Error parsing JSON: %v", err)
        return err
    }

    dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/user_management", dbCreds.Username, dbCreds.Password, dbCreds.Host, dbCreds.Port)
    db, err = sql.Open("mysql", dsn)
    if err != nil {
        log.Printf("Error opening database: %v", err)
        return err
    }

    // Setting up the connection pool
    db.SetMaxOpenConns(10)
    db.SetMaxIdleConns(5)
    db.SetConnMaxLifetime(0) // Connections are recycled forever

    if err = db.Ping(); err != nil {
        log.Printf("Failed to connect to database: %v", err)
        return err
    }

    fmt.Println("Connected to the MySQL database successfully!")
    return nil
}

func updateUserBalance(userID string, amount int64) error {
	// Amount is in cents, convert:
	amountInDollars := float64(amount) / 100.0
    query := `UPDATE Users SET AccountBalance = AccountBalance + ? WHERE UserID = ?`
    _, err := db.Exec(query, amountInDollars, userID)
    if err != nil {
        return fmt.Errorf("updateUserBalance: %v", err)
    }
    return nil
}

func updateTransactionHistory(transactionID, status string, transactionDate time.Time) error {
    query := `UPDATE TransactionHistory SET TransactionStatus = ?, TransactionDate = ? WHERE TransactionID = ?`
    _, err := db.Exec(query, status, transactionDate, transactionID)
    if err != nil {
        return fmt.Errorf("updateTransactionHistory: %v", err)
    }
    return nil
}

// webhook() processes incoming Stripe webhook events via AWS API Gateway.
// If the event type is "payment_intent.succeeded", it updates the transaction history in the database to mark the 
// transaction as completed and adjusts the user's balance according to the amount specified in the
// webhook event. The function responds to the API Gateway with a message indicating
// successful handling of the webhook.
//
// Parameters:
// - ctx: Context associated with the request, used for managing cancellation signals and deadlines.
// - request: The incoming request object from API Gateway containing the webhook data.
//
// Returns:
// - APIGatewayProxyResponse: Struct containing the HTTP status code and response body. This is used
//   by API Gateway to form the HTTP response.
// - error: Error object that will be nil if successful, or contains an error
//   message if an error occurs.
func webhook(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {

	var webhookEvent StripeWebhookEvent

    err := json.Unmarshal([]byte(request.Body), &webhookEvent)
    if err != nil {
        fmt.Printf("Error unmarshaling JSON: %v\n", err)
        return events.APIGatewayProxyResponse{
            StatusCode: http.StatusInternalServerError,
            Body:       "Error processing request",
        }, nil
    }

    fmt.Printf("Event Type: %s\n", webhookEvent.Type)
    fmt.Printf("Transaction ID: %s\n", webhookEvent.Data.Object.ID)
    fmt.Printf("Amount: %d\n", webhookEvent.Data.Object.Amount)
    fmt.Printf("Currency: %s\n", webhookEvent.Data.Object.Currency)
    fmt.Printf("Description: %s\n", webhookEvent.Data.Object.Description)
    fmt.Printf("Customer ID: %s\n", webhookEvent.Data.Object.Customer)

	if webhookEvent.Type == "payment_intent.succeeded"{
		// Update transaction history 
        if err := updateTransactionHistory(webhookEvent.Data.Object.ID, "Completed", time.Now()); err != nil {
            fmt.Printf("Error updating transaction history: %v\n", err)
            return events.APIGatewayProxyResponse{
                StatusCode: http.StatusInternalServerError,
                Body:       "Failed to update transaction history",
            }, nil
        }

        // Update user balance
        if err := updateUserBalance(request.RequestContext.Identity.CognitoIdentityPoolID, webhookEvent.Data.Object.Amount); err != nil {
            fmt.Printf("Error updating user balance: %v\n", err)
            return events.APIGatewayProxyResponse{
                StatusCode: http.StatusInternalServerError,
                Body:       "Failed to update user balance",
            }, nil
        }

		return events.APIGatewayProxyResponse{
			StatusCode: http.StatusOK,
			Body:       fmt.Sprintf("Received confirmation from Stripe: %s", webhookEvent.Data.Object.Description),
		}, nil
	}

	return events.APIGatewayProxyResponse{
        StatusCode: http.StatusOK,
        Body:       fmt.Sprintf("Received confirmation from Stripe: %s", webhookEvent.Data.Object.Description),
    }, nil
}

func main() {
    region := "us-west-2"
    paramName := "/application/dev/stripe_key"
	var err error

    awsParams.stripeKey, err = getParameter(region, paramName)
    if err != nil {
        log.Fatalf("Failed to get parameter: %v", err)
    }
    log.Printf("Successfully retrieved stripe key!")

    if err := initializeDatabase(); err != nil {
        log.Fatalf("Database initialization failed: %v", err)
    }
	// lambda.Start(handler)

	file, err := os.ReadFile("event.json")
    if err != nil {
        fmt.Printf("Failed to read file: %s\n", err)
        return
    }

    // Unmarshal the JSON into an APIGatewayProxyRequest
    var request events.APIGatewayProxyRequest
    err = json.Unmarshal(file, &request)
    if err != nil {
        fmt.Printf("Failed to unmarshal request: %s\n", err)
        return
    }

    // Call the handler with the unmarshalled request
    ctx := context.Background()
    response, err := webhook(ctx, request)
    if err != nil {
        fmt.Printf("Handler error: %s\n", err)
        return
    }

    // Print the response
    fmt.Printf("Handler response: %+v\n", response)
}


