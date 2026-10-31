//go:generate go run go.uber.org/mock/mockgen@latest -source=step_up.go -destination=step_up_mocks_test.go -package=auth -write_package_comment=false

package auth

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/partyzanex/padmark/internal/domain"
)

// StepUp is the re-authentication a caller holding only a bearer API token presents to issue a
// credential: the account password and a fresh TOTP code (replay-protected like a login).
type StepUp struct {
	Password string
	TOTPCode string
}

// Issuer describes how the caller of a credential-issuing operation (API token, invite)
// authenticated. A browser session issues directly. A bearer API token must also present a
// StepUp — otherwise one leaked key could mint any number of siblings that outlive its
// revocation and a password change.
type Issuer struct {
	// APITokenHash is the TokenHash of the bearer API token the request used; "" for a session.
	APITokenHash string
	StepUp       StepUp
}

// SessionIssuer is the Issuer of a browser-session request: no bearer key, so no step-up is due.
func SessionIssuer() Issuer {
	return Issuer{APITokenHash: "", StepUp: StepUp{Password: "", TOTPCode: ""}}
}

// StepUpVerifier re-authenticates a user with their password and a fresh TOTP code. Shared by
// APITokenManager and OnboardingManager; implemented by SessionManager, which owns the login
// factors. Fail-hard: any error refuses the issuance.
type StepUpVerifier interface {
	// VerifyStepUp returns domain.ErrStepUpFailed for a wrong password or a wrong/replayed code.
	VerifyStepUp(ctx context.Context, userID uuid.UUID, stepUp StepUp) error
}

// authorizeIssuer enforces the issuing rule for userID: a session issues directly, a bearer token
// only with a verified step-up (domain.ErrStepUpRequired when none was sent).
func authorizeIssuer(ctx context.Context, verifier StepUpVerifier, userID uuid.UUID, issuer Issuer) error {
	if issuer.APITokenHash == "" {
		return nil
	}

	if issuer.StepUp.Password == "" || issuer.StepUp.TOTPCode == "" {
		return domain.ErrStepUpRequired
	}

	err := verifier.VerifyStepUp(ctx, userID, issuer.StepUp)
	if err != nil {
		return fmt.Errorf("verify step-up: %w", err)
	}

	return nil
}
