package usage

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
)

var zaiErrorMessages = map[string]string{
	"1000": "Authentication failed. Check your API key.",
	"1001": "Authentication header missing. Check your API key.",
	"1003": "Authentication token expired. Obtain a new token.",
	"1005": "Two-factor authentication required.",
	"1113": "Insufficient balance or no resource package. Recharge your account.",
	"1200": "API call error. Try again later.",
	"1210": "Invalid API parameter. Check the API documentation.",
	"1211": "Unknown model. Check the model ID.",
	"1212": "This model does not support the requested method.",
	"1213": "A required parameter is missing.",
	"1214": "Invalid parameter. Check the API documentation.",
	"1215": "Conflicting parameters. Check the API documentation.",
	"1220": "Access denied. Check your permissions.",
	"1221": "This API has been taken offline.",
	"1222": "This API does not exist.",
	"1230": "API processing error. Try again later.",
	"1234": "Network error. Try again later.",
	"1261": "Prompt too long. Reduce the input length.",
	"1301": "Content rejected by the safety policy.",
	"1302": "Request rate limit reached. Try again later.",
	"1305": "Service overloaded. Try again later.",
	"1308": "Usage limit reached. Wait for the quota reset.",
	"1309": "GLM Coding Plan expired. Renew your subscription.",
	"1310": "Weekly or monthly limit exhausted. Wait for the quota reset.",
	"1311": "Your subscription does not include this model.",
	"1313": "Request frequency restricted by the Fair Usage Policy. Contact support.",
	"1314": "Enterprise package expired. Contact your administrator.",
	"1315": "This API key requires an enterprise coding package scenario. Replace the key.",
	"1316": "5-hour limit reached; insufficient balance for extra usage. Wait for reset.",
	"1317": "7-day limit reached; insufficient balance for extra usage. Wait for reset.",
	"1318": "5-hour limit reached; extra usage blocked by monthly spend limit.",
	"1319": "7-day limit reached; extra usage blocked by monthly spend limit.",
	"1320": "5-hour limit reached; extra usage blocked by monthly spend limit.",
	"1321": "7-day limit reached; extra usage blocked by monthly spend limit.",
}
var zaiHTTPMessages = map[string]string{
	"400": "Invalid request. Check the API documentation.",
	"401": "Authentication failed. Check your API key.",
	"403": "Access denied. Check your permissions.",
	"429": "Request or usage limit reached. Try again later.",
	"500": "Internal error. Try again later.",
}
var zaiCodePattern = regexp.MustCompile(`^(?:0|[1-9]\d{0,3})$`)

func zaiErrorCode(value any) string {
	if number, ok := value.(float64); ok && number >= 0 && number <= 9999 && math.Trunc(number) == number {
		return strconv.Itoa(int(number))
	}
	if text, ok := value.(string); ok && zaiCodePattern.MatchString(text) {
		return text
	}
	return ""
}

func zaiPayloadError(value any) string {
	payload := object(value)
	nested := object(payload["error"])
	raw, present := nested["code"]
	if !present {
		raw, present = payload["code"]
	}
	code := zaiErrorCode(raw)
	_, hasError := payload["error"]
	if !hasError && payload["success"] != false && (!present || code == "0" || code == "200") {
		return ""
	}
	if code != "" && code != "0" && code != "200" {
		message := zaiErrorMessages[code]
		if message == "" {
			message = "API request failed."
		}
		return "Z.AI " + code + ": " + message
	}
	return "Z.AI: API request failed."
}

func zaiResponseError(status int, body []byte) string {
	var value any
	if err := json.Unmarshal(body, &value); err != nil && status >= 200 && status < 300 {
		return "Z.AI: Invalid JSON response."
	}
	if message := zaiPayloadError(value); message != "" {
		return message
	}
	if status < 200 || status >= 300 {
		message := zaiHTTPMessages[strconv.Itoa(status)]
		if message == "" {
			message = "API request failed."
		}
		return fmt.Sprintf("Z.AI HTTP %d: %s", status, message)
	}
	return ""
}
