package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestReader_Sees(t *testing.T) {
	t.Parallel()

	owner := uuid.New()
	other := uuid.New()

	tests := []struct {
		name    string
		reader  Reader
		privacy Privacy
		ownerID *uuid.UUID
		want    bool
	}{
		{"public for anonymous", Reader{uuid.Nil, false, false, false}, PrivacyPublic, nil, true},
		{"authenticated for anonymous", Reader{uuid.Nil, false, false, false}, PrivacyAuthenticated, nil, false},
		{"authenticated for signed-in", Reader{other, true, false, false}, PrivacyAuthenticated, nil, true},
		{"owner for owner", Reader{owner, true, false, false}, PrivacyOwner, &owner, true},
		{"owner for other", Reader{other, true, false, false}, PrivacyOwner, &owner, false},
		{"owner without owner", Reader{uuid.Nil, true, false, false}, PrivacyOwner, nil, false},
		{"unknown level fails closed", Reader{owner, true, false, false}, Privacy("secret"), &owner, false},
		{"accounts off sees owner-only", Reader{uuid.Nil, false, false, true}, PrivacyOwner, &owner, true},
		{"accounts off sees unknown level", Reader{uuid.Nil, false, false, true}, Privacy("secret"), nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.reader.Sees(tt.privacy, tt.ownerID))
		})
	}
}
