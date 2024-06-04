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
	"github.com/stripe/stripe-go"
	"github.com/stripe/stripe-go/paymentintent"
)

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

func insertTransaction(transactionID, userID, transactionType, transactionStatus, transactionDate string, amount float64) error {
    query := `INSERT INTO TransactionHistory (TransactionID, UserID, TransactionType, Amount, TransactionStatus, TransactionDate) 
              VALUES (?, ?, ?, ?, ?, ?)`

    _, err := db.Exec(query, transactionID, userID, transactionType, amount, transactionStatus, transactionDate)
    if err != nil {
        return fmt.Errorf("error inserting new transaction: %w", err)
    }

    log.Printf("Inserted new transaction record successfully for user ID %s", userID)
    return nil
}

// confirmPayment confirms a Stripe payment intent based on the request.
// It attempts to confirm the payment intent and handles various outcomes based on the payment intent's status.
// This function is triggered via an API Gateway event that passes in the request containing
// the payment intent ID and any necessary parameters.
//
// Parameters:
// - ctx: Provides context for the function, allowing handling of timeouts and cancellation signals.
// - request: An APIGatewayProxyRequest struct that contains the HTTP request data, including the body
//            that should have the PaymentIntentID necessary for identifying the payment intent to confirm.
//
// Returns:
// - APIGatewayProxyResponse: A struct that encapsulates the HTTP response data, including status codes
//                            and response bodies tailored to the result of the confirmation process.
// - error: An error object that is non-nil if an error occurs during the function's execution, such as
//          failure to parse the request body or errors from the Stripe API.
//>
// Behavior:
// - The function first parses the incoming JSON request body to extract the PaymentIntentID.
// - It then attempts to confirm the payment intent using Stripe's API.
// - Based on the Stripe payment intent status after confirmation attempt, it handles:
//   - stripe.PaymentIntentStatusRequiresAction: Notifies the client that additional user action is needed (e.g. 3D Secure), but unsure if we'll
//      3D secure, so I'm just leaving that for now.
//   - stripe.PaymentIntentStatusSucceeded: Logs the transaction as "Pending" in the database and informs the client of a pending status.
//   - stripe.PaymentIntentStatusRequiresConfirmation: Attempts to re-confirm the payment if the initial attempt was failed.
//   - Default: Handles any unanticipated statuses by returning an error message and the status of the payment intent.
func confirmPayment(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
    stripe.Key = awsParams.stripeKey

    var body ConfirmPaymentRequest
    if err := json.Unmarshal([]byte(request.Body), &body); err != nil {
        return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, err
    }

    pi, err := paymentintent.Confirm(body.PaymentIntentID, nil)
    if err != nil {
        return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
    }

    switch pi.Status {
        case stripe.PaymentIntentStatusRequiresAction:
            return events.APIGatewayProxyResponse{
                StatusCode: 200,
                Body:       "Additional authentication required. Possible issues with 3D secure auth \n " + string(pi.Status),
            }, nil

        case stripe.PaymentIntentStatusSucceeded:
            currentTime := time.Now()
            insertTransaction(pi.ID, request.RequestContext.Identity.CognitoIdentityPoolID, "Deposit", "Pending", currentTime.GoString(), float64(pi.Amount))
            return events.APIGatewayProxyResponse{
                StatusCode: 200,
                Body:       "Payment succeeded and is pending. Funds will be available once payment is confrimed from Stripe.",
            }, nil

        case stripe.PaymentIntentStatusRequiresConfirmation:
            // Re-confirm the payment intent if needed
            piAttemptTwo, err := paymentintent.Confirm(pi.ID, nil)
            if err != nil {
                return events.APIGatewayProxyResponse{
                    StatusCode: 500,
                    Body:       "Failed to confirm payment intent",
                }, nil
            }

            if piAttemptTwo.Status == stripe.PaymentIntentStatusSucceeded {
                currentTime := time.Now()
                insertTransaction(pi.ID, request.RequestContext.Identity.CognitoIdentityPoolID, "Deposit", "Pending", currentTime.GoString(), float64(piAttemptTwo.Amount))
                return events.APIGatewayProxyResponse{
                    StatusCode: 200,
                    Body:       "Payment succeeded and is pending. Funds will be available once payment is confrimed from Stripe.",
                }, nil
            } else {
                return events.APIGatewayProxyResponse{
                    StatusCode: 400,
                    Body:       "Tried to confirm the payment again, but failed... \n" + string(piAttemptTwo.Status),
                }, nil
            }
            
        default:
            return events.APIGatewayProxyResponse{
                StatusCode: 400,
                Body:       "Unhandled payment intent status \n " + string(pi.Status),
            }, nil
    }
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
    response, err := confirmPayment(ctx, request)
    if err != nil {
        fmt.Printf("Handler error: %s\n", err)
        return
    }

    // Print the response
    fmt.Printf("Handler response: %+v\n", response)

    //lambda.Start(confirmPayment)
}


