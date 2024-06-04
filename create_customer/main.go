package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ssm"
	_ "github.com/go-sql-driver/mysql"
	"github.com/stripe/stripe-go/v72"
	"github.com/stripe/stripe-go/v72/customer"
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

// Struct to keep the secret key and more params if needed
type AWSParams struct {
	stripeKey string
}

// Globals 
var db *sql.DB
var awsParams AWSParams

func findCustomerByStripeID(customerID string) (*stripe.Customer, error) {
    c, err := customer.Get(customerID, nil)
    if err != nil {
        return nil, err
    }
    // Return the found customer
    return c, nil
}

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

// handler() manages the getting and creation of customer data both in the local database and on Stripe.
// It uses the user's Cognito Identity Pool ID to fetch user details from the database, checks if the user is already registered
// as a Stripe customer, and if not, creates a new Stripe customer record using their details. It then updates the local database
// with the new Stripe customer ID.
//
// Parameters:
// - ctx: Context for managing request deadlines and cancellation signals.
// - request: The APIGatewayProxyRequest containing the AWS Lambda request data, including user identity.
//
// Returns:
// - APIGatewayProxyResponse: Contains the HTTP status code, response body, and headers.
// - error: Provides details on any errors encountered during the function's execution. Returns nil if the operation is successful.
//
// createCustomer():
// 1. Retrieves user details from the database using the Cognito Identity Pool ID provided in the request context.
// 2. Checks if the user already has a Stripe customer ID and fetches the Stripe customer details if available.
// 3. If the user is not already a Stripe customer, creates a new Stripe customer record with the user's email and name.
// 4. Updates the local database with the new Stripe customer ID after successful creation.
// 5. Returns a response indicating the outcome of the operations, including successful creation or error messages.
//
// Usage:
// This function is to be triggered via an API Gateway request. It mantains consistent
// customer records across both a local database and Stripe; the function ensures that every registered user in the
// local database is also registered as a customer in Stripe.
func createCustomer(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
    stripe.Key = awsParams.stripeKey

    userID := request.RequestContext.Identity.CognitoIdentityPoolID
    query := "SELECT * FROM Users WHERE UserID = ?"
    
    var user User
    err := db.QueryRow(query, userID).Scan(&user.UserID, &user.Username, &user.Email, &user.PhoneNumber, &user.DateOfBirth, &user.AccountVerificationStatus, &user.CreatedAt, &user.UpdatedAt, &user.AccountBalance, &user.StripeID)
    if err != nil {
        return events.APIGatewayProxyResponse{
            StatusCode: http.StatusInternalServerError,
            Body:       "Error retrieving user from database",
            Headers:    map[string]string{"Content-Type": "application/json"},
        }, err
    }

    if user.StripeID != nil {
        customer, err := findCustomerByStripeID(*user.StripeID)
        if err != nil {
            return events.APIGatewayProxyResponse{
                StatusCode: http.StatusInternalServerError,
                Body:       fmt.Sprintf("Error finding customer by Stripe ID: %s", err),
                Headers:    map[string]string{"Content-Type": "application/json"},
            }, err
        }
        if customer != nil {
            return events.APIGatewayProxyResponse{
                StatusCode: http.StatusOK,
                Body:       fmt.Sprintf("Customer already exists with Stripe ID: %s", customer.ID),
                Headers:    map[string]string{"Content-Type": "application/json"},
            }, nil
        }
    }

    params := &stripe.CustomerParams{
        Email: stripe.String(user.Email),
        Name:  stripe.String(user.Username),
    }

    // Customer parameter in Stripe to keep a record of User IDs from the database
    params.AddMetadata("UserID", userID)

    c, err := customer.New(params)
    if err != nil {
        return events.APIGatewayProxyResponse{
            StatusCode: http.StatusInternalServerError,
            Body:       fmt.Sprintf("Error creating new customer in Stripe: %s", err),
            Headers:    map[string]string{"Content-Type": "application/json"},
        }, err
    }

    // Add the stripe customer ID to the database
    updateQuery := "UPDATE Users SET stripe_customer_id = ? WHERE UserID = ?"
    _, err = db.Exec(updateQuery, c.ID, userID)
    if err != nil {
        return events.APIGatewayProxyResponse{
            StatusCode: http.StatusInternalServerError,
            Body:       "Error updating user with new Stripe ID",
            Headers:    map[string]string{"Content-Type": "application/json"},
        }, err
    }

    return events.APIGatewayProxyResponse{
        StatusCode: http.StatusOK,
        Body:       fmt.Sprintf("Created new customer in Stripe with ID: %s", c.ID),
        Headers:    map[string]string{"Content-Type": "application/json"},
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
	// lambda.Start(createCustomer)

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
    response, err := createCustomer(ctx, request)
    if err != nil {
        fmt.Printf("Handler error: %s\n", err)
        return
    }

    // Print the response
    fmt.Printf("Handler response: %+v\n", response)

}