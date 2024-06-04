This service handles various Stripe payment operations using AWS services and Stripe. The service retrieves sensitive parameters like the Stripe API key from AWS Systems Manager (SSM) and interacts with a MySQL database to manage user information.

When a payment is initiated, the createPaymentIntent function creates a new payment intent in Stripe and links it to the customer. Next, the confirmPayment function handles the confirmation of the payment intent. The webhook function processes incoming events from Stripe, such as payment confirmations, and updates user balances in the MySQL database.

## Prerequisites

- Go 1.16 or later
- AWS account with necessary permissions to use Lambda and SSM
- Stripe account and API key
- MySQL database

## Installation

1. Clone the repository:

   \`\`\`sh
   git clone https://github.com/yourusername/stripe-payments-service.git
   cd stripe-payments-service
   \`\`\`

2. Install dependencies:

   \`\`\`
   go mod tidy
   \`\`\`

## Configuration

### AWS Systems Manager (SSM) Parameters

Ensure the following parameters are stored in AWS SSM Parameter Store:

- \`/application/dev/stripe_key\`: Stripe API key.
- \`/application/dev/database/credentials\`: Database connection string.

### Database Initialization

The database initialization code expects the credentials to be stored in AWS SSM. Modify the parameter name in the code if necessary.

## Usage

1. Build the Go executable:

   \`\`\`sh
   go build -o main main.go
   \`\`\`

2. To test the function locally, create a JSON file named \`event.json\` with a sample API Gateway event. Example:

   \`\`\`json
   {
   "resource": "/user/{userId}",
   "path": "/user/12345",
   "httpMethod": "GET",
   "headers": {
   "Accept": "_/_",
   "Host": "your-api-id.execute-api.region.amazonaws.com",
   "User-Agent": "YourUserAgentString",
   "X-Amzn-Trace-Id": "Root=1-23456789-abcdef0123456789abcdef0"
   },
   "multiValueHeaders": {
   "Accept": ["*/*"],
   "User-Agent": ["YourUserAgentString"]
   },
   "queryStringParameters": null,
   "multiValueQueryStringParameters": null,
   "pathParameters": {
   "userId": "1"
   },
   "stageVariables": null,
   "requestContext": {
   "resourceId": "abcd12",
   "resourcePath": "/user/{userId}",
   "httpMethod": "GET",
   "extendedRequestId": "abcdef123456",
   "requestTime": "01/Feb/2024:12:34:56 +0000",
   "path": "/dev/user/12345",
   "accountId": "123456789012",
   "protocol": "HTTP/1.1",
   "stage": "dev",
   "domainPrefix": "your-api-id",
   "requestTimeEpoch": 1580557496123,
   "requestId": "abcdefgh-1234-5678-abcd-1234567890ab",
   "identity": {
   "cognitoIdentityPoolId": "7",
   "accountId": null,
   "cognitoIdentityId": null,
   "caller": null,
   "sourceIp": "123.123.123.123",
   "principalOrgId": null,
   "accessKey": null,
   "cognitoAuthenticationType": null,
   "cognitoAuthenticationProvider": null,
   "userArn": null,
   "userAgent": "YourUserAgentString",
   "user": null
   },
   "domainName": "your-api-id.execute-api.region.amazonaws.com",
   "apiId": "your-api-id"
   },
   "body": null,
   "isBase64Encoded": false
   }

   \`\`\`

3. Run the service:

   \`\`\`sh
   ./main
   \`\`\`

## Functions

### \`getParameter\`

Retrieves a parameter from AWS SSM.

\`\`\`go
func getParameter(region, paramName string) (string, error)
\`\`\`

### \`initializeDatabase\`

Initializes the database connection using credentials retrieved from AWS SSM.

\`\`\`go
func initializeDatabase() error
\`\`\`

### \`findCustomerByStripeID\`

Finds a Stripe customer by their Stripe ID.

\`\`\`go
func findCustomerByStripeID(customerID string) (\*stripe.Customer, error)
\`\`\`

### \`createCustomer\`

Creates a new customer in Stripe and updates the user's Stripe ID in the database.

\`\`\`go
func createCustomer(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error)
\`\`\`

### \`createPaymentIntent\`

Creates a new payment intent in Stripe.

\`\`\`go
func createPaymentIntent(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error)
\`\`\`

### \`confirmPayment\`

Handles the confirmation of a Stripe payment intent and returns the result.

\`\`\`go
func confirmPayment(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error)
\`\`\`

### \`webhook\`

Handles incoming Stripe webhook events and updates user balances accordingly.

\`\`\`go
func webhook(ctx context.Context, request events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error)
\`\`\`

### \`main\`

The entry point of the service. Retrieves necessary parameters, initializes the database, and runs the Lambda function.

\`\`\`go
func main()
\`\`\`

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
"""
