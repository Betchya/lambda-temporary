package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stripe/stripe-go"
	"github.com/stripe/stripe-go/paymentintent"
	"github.com/stripe/stripe-go/paymentmethod"
)

// Struct representing how a User looks in the database
type User struct {
    UserID                    string
    Username                  string
    Email                     string
    PhoneNumber               string
    DateOfBirth               string
    AccountVerificationStatus string
    CreatedAt                 string
    UpdatedAt                 string
    AccountBalance            string
    StripeID*                 string // Is null if the user is not a Stripe customer
}

// These values should come from the frontend
type PaymentIntentRequest struct {
    Amount         int64   `json:"amount"`
    Currency       string  `json:"currency"`
    PaymentMethodID string `json:"PaymentMethodID"`
}

// Struct for secret params from AWS Parameter Store
type AWSParams struct {
	stripeKey string
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

// This function extracts payment details from the incoming APIGatewayProxyRequest, which should include a PaymentMethodID recieved from Stripe,
// retrieves the user's Stripe customer ID from the database,
// and uses this information to initiate a payment process using Stripe's API. It handles errors throughout the process,
// such as JSON parsing errors, database retrieval errors, and failures in creating the payment intent with Stripe.
//
// Parameters:
// - ctx: Context for managing request deadlines and cancellation signals.
// - request: The APIGatewayProxyRequest from AWS Lambda which includes user and payment data.
//
// Returns:
// - APIGatewayProxyResponse: Struct containing the HTTP status code, body, and headers of the response.
// - error: Error object detailing any issues encountered during the execution of the function. If the operation is successful, the error is nil.
//
// createPaymentIntent():
// 1. Parses the request body to extract payment details.
// 2. Queries the database for the user's details using their Cognito Identity Pool ID from the request context.
// 3. Validates that the user has a Stripe customer ID.
// 4. Creates a payment intent with Stripe using the user's payment details and customer ID.
// 5. Returns a success response with the payment intent ID or an error message detailing any issues encountered.
//
// Usage:
// This function is intended to be triggered via AWS API Gateway as part of a serverless architecture,
// used for secure payment processing. 
func createPaymentIntent(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
    stripe.Key = awsParams.stripeKey

    var paymentIntent PaymentIntentRequest
    if err := json.Unmarshal([]byte(request.Body), &paymentIntent); err != nil {
        return events.APIGatewayProxyResponse{StatusCode: http.StatusBadRequest}, err
    }

    userID := request.RequestContext.Identity.CognitoIdentityPoolID
    query := "SELECT * FROM Users WHERE UserID = ?"
    
    // Get the stripe customer ID from the DB 
    var user User
    err := db.QueryRow(query, userID).Scan(&user.UserID, &user.Username, &user.Email, &user.PhoneNumber, &user.DateOfBirth, &user.AccountVerificationStatus, &user.CreatedAt, &user.UpdatedAt, &user.AccountBalance, &user.StripeID)
    if err != nil {
        return events.APIGatewayProxyResponse{
            StatusCode: http.StatusInternalServerError,
            Body:       "Error retrieving user from database",
            Headers:    map[string]string{"Content-Type": "application/json"},
        }, err
    }

    // The user should have a stripe cutsomer ID in the database
    if user.StripeID == nil {                               
        return events.APIGatewayProxyResponse{
            StatusCode: http.StatusOK,
            Body:       "Customer does not have a Stripe customer ID. Are they registered as a customer?",
            Headers:    map[string]string{"Content-Type": "application/json"},
        }, nil
    }

    // Attach the PaymentMethod to the Customer if not already attached
    _, err = paymentmethod.Attach(
        paymentIntent.PaymentMethodID,
        &stripe.PaymentMethodAttachParams{
            Customer: stripe.String(*user.StripeID),
        },
    )
    
    if err != nil {
        log.Printf("Error attaching payment method: %v", err)
        return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
    }

    params := &stripe.PaymentIntentParams{
        Amount:   stripe.Int64(paymentIntent.Amount),
        Currency: stripe.String(paymentIntent.Currency),
        Customer: stripe.String(*user.StripeID),
        PaymentMethod: stripe.String(paymentIntent.PaymentMethodID),
        SetupFutureUsage: stripe.String("off_session"),
    }

    pi, err := paymentintent.New(params)
    if err != nil {
        log.Printf("Error creating payment intent: %v", err)
        return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, err
    }

    response, err := json.Marshal(map[string]string{"payment_intent_id": pi.ID})

    if err != nil {
        log.Printf("Error marshaling response: %v", err)
        return events.APIGatewayProxyResponse{StatusCode: http.StatusInternalServerError}, errors.New("internal server error")
    }

    return events.APIGatewayProxyResponse{
        StatusCode: http.StatusOK,
        Body:       string(response),
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
    response, err := createPaymentIntent(ctx, request)
    if err != nil {
        fmt.Printf("Handler error: %s\n", err)
        return
    }

    // Print the response
    fmt.Printf("Handler response: %+v\n", response)

    //lambda.Start(createPaymentIntent)
}
