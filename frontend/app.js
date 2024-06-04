document.addEventListener("DOMContentLoaded", function () {
  var stripe = Stripe(
    "pk_test_51P3ilQHPfdwb9Dagj4J267rMwtxpCHvwBW2PloksRrvBLvIkEJGA6ZUNjXdfUI5OWO0akRYlcIrzz8G6W3gODw0s00qGuD5LmN"
  ); // Replace with your actual publishable key
  var elements = stripe.elements();

  var card = elements.create("card");
  card.mount("#card-element");

  card.addEventListener("change", function (event) {
    var displayError = document.getElementById("card-errors");
    if (event.error) {
      displayError.textContent = event.error.message;
    } else {
      displayError.textContent = "";
    }
  });

  var form = document.getElementById("payment-form");
  form.addEventListener("submit", function (event) {
    event.preventDefault();

    stripe
      .createPaymentMethod({
        type: "card",
        card: card,
        billing_details: {
          name: "Jenny Rosen", // Placeholder name
        },
      })
      .then(function (result) {
        if (result.error) {
          // Display error in your UI
          document.getElementById("payment-result").textContent =
            result.error.message;
        } else {
          // Display the PaymentMethod ID in the browser
          document.getElementById("payment-result").textContent =
            "PaymentMethod ID: " + result.paymentMethod.id;
        }
      });
  });
});
