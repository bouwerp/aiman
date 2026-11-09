package awslogin

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

// verifyClientName is the OIDC client name of a start URL verification.
const verifyClientName = "aiman-permission-set-apply"

// ErrStartURLRejected is StartDeviceAuthorization refusing the start URL. It is
// awsdelegation's value, so Apply matches it without importing this package.
var ErrStartURLRejected = awsdelegation.ErrStartURLRejected

// Verifier runs the two OIDC calls in process. Zero values: a 30s http.Client
// and https://oidc.<region>.amazonaws.com. It never builds a Manager or touches
// the registration file: the client secret lives in memory for the one call.
type Verifier struct {
	HTTP *http.Client
	OIDC func(region string) string
}

// VerifyStartURL is Verifier{}.VerifyStartURL.
func VerifyStartURL(ctx context.Context, region, startURL string) error {
	return Verifier{}.VerifyStartURL(ctx, region, startURL)
}

// VerifyStartURL registers a public client and starts a device authorization
// for startURL. A device code in the answer means the regional OIDC endpoint
// accepts the URL as an access portal; the authorization is never completed
// and expires on its own. A 400 InvalidRequestException from
// StartDeviceAuthorization is ErrStartURLRejected. Errors carry the operation,
// status and error code only, never the client secret or the device code.
func (v Verifier) VerifyStartURL(ctx context.Context, region, startURL string) error {
	if !regionName.MatchString(region) {
		return fmt.Errorf("awslogin: %q is not an AWS region name", region)
	}
	client := noRedirectClient(v.HTTP)
	base := v.oidcBase(region)
	var reg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	in := map[string]string{"clientName": verifyClientName, "clientType": clientType}
	if err := postJSON(ctx, client, "RegisterClient", base+"/client/register", in, &reg); err != nil {
		return err
	}
	if reg.ClientID == "" || reg.ClientSecret == "" {
		return &apiError{op: "RegisterClient", status: http.StatusOK, code: codeParse}
	}
	const op = "StartDeviceAuthorization"
	var dev struct {
		DeviceCode string `json:"deviceCode"`
	}
	in = map[string]string{"clientId": reg.ClientID, "clientSecret": reg.ClientSecret, "startUrl": startURL}
	if err := postJSON(ctx, client, op, base+"/device_authorization", in, &dev); err != nil {
		if startURLRejected(err) {
			return fmt.Errorf("%w: %w", ErrStartURLRejected, err)
		}
		return err
	}
	if dev.DeviceCode == "" {
		return &apiError{op: op, status: http.StatusOK, code: codeParse}
	}
	return nil
}

func (v Verifier) oidcBase(region string) string {
	if v.OIDC != nil {
		return v.OIDC(region)
	}
	return "https://oidc." + region + ".amazonaws.com"
}

// startURLRejected reports a 400 InvalidRequestException, from the
// x-amzn-ErrorType header or the body's OAuth invalid_request.
func startURLRejected(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.status == http.StatusBadRequest && ae.code == codeInvalidReq
}
