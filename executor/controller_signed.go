package executor

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// SignedControllerCapability identifies local, public-key admission verification.
const SignedControllerCapability = "controller-signed.v1"

// ControllerAdmissionLifetime bounds offline admission, not live ownership.
// Trusted hosts check live authority before each operation. An offline executor
// can order only delivered generations; a delayed signed admission can be stale
// in its issuer's database without being stale against the local high-water mark.
const ControllerAdmissionLifetime = 2 * time.Hour

// ControllerServerConfig contains public verification material and a narrow
// identity/artifact bearer. Neither grants signing or workspace mutation authority.
type ControllerServerConfig struct {
	ResourceID       string   `json:"resource_id"`
	Incarnation      string   `json:"incarnation"`
	VerificationKey  []byte   `json:"verification_key"`
	ArtifactToken    string   `json:"artifact_token"`
	BootstrapSHA256  string   `json:"bootstrap_sha256,omitempty"`
	BootstrapCommand []string `json:"bootstrap_command,omitempty"`
}

// ControllerAdmission binds a signed generation to one exact process and purpose.
// Owner/Run/Task are opaque issuer coordinates, not Sol authorization policy.
type ControllerAdmission struct {
	Protocol string             `json:"protocol"`
	Identity ControllerIdentity `json:"identity"`
	Grant    ControllerGrant    `json:"grant"`
	Run      string             `json:"run"`
	Task     string             `json:"task,omitempty"`
	Owner    string             `json:"owner"`
	Purpose  string             `json:"purpose"`
	SHA256   string             `json:"sha256,omitempty"`
	IssuedAt time.Time          `json:"issued_at"`
}

func (a ControllerAdmission) valid() bool {
	now := time.Now()
	digest, err := hex.DecodeString(a.SHA256)
	purpose := a.Purpose == "execute" && a.SHA256 == "" || a.Purpose == "initialize" && err == nil && len(digest) == 32
	return a.Protocol == SignedControllerCapability && a.Identity.ResourceID != "" && a.Identity.Incarnation != "" && a.Identity.BootID != "" && a.Grant.Epoch > 0 && a.Grant.AdmissionID != "" && a.Run != "" && a.Owner != "" && purpose && !a.IssuedAt.IsZero() && !a.IssuedAt.After(now) && a.Grant.ValidUntil.After(now) && a.Grant.ValidUntil.After(a.IssuedAt) && a.Grant.ValidUntil.Sub(a.IssuedAt) <= ControllerAdmissionLifetime
}

// SignControllerAdmission is an issuer operation. The private key stays outside
// the executor container. Issuers must admit the exact live owner/resource first.
func SignControllerAdmission(key ed25519.PrivateKey, admission ControllerAdmission) (string, error) {
	if len(key) != ed25519.PrivateKeySize || !admission.valid() {
		return "", errors.New("executor: invalid signed controller admission")
	}
	data, err := json.Marshal(admission)
	if err != nil {
		return "", err
	}
	signature := ed25519.Sign(key, append([]byte(SignedControllerCapability+"\x00"), data...))
	return base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

type SignedControllerAuthority struct{ config ControllerServerConfig }

func NewSignedControllerAuthority(config ControllerServerConfig) (*SignedControllerAuthority, error) {
	if config.ResourceID == "" || config.Incarnation == "" || len(config.VerificationKey) != ed25519.PublicKeySize || config.ArtifactToken == "" {
		return nil, errors.New("executor: complete signed controller configuration required")
	}
	config.VerificationKey = append([]byte(nil), config.VerificationKey...)
	return &SignedControllerAuthority{config: config}, nil
}

func (a *SignedControllerAuthority) Validate(ctx context.Context, identity ControllerIdentity, credential string) (ControllerGrant, error) {
	return a.validate(ctx, identity, credential, "execute", "")
}

func (a *SignedControllerAuthority) ValidateBootstrap(ctx context.Context, identity ControllerIdentity, credential, digest string) (ControllerGrant, error) {
	if digest == "" || digest != a.config.BootstrapSHA256 {
		return ControllerGrant{}, errors.New("executor: bootstrap digest mismatch")
	}
	return a.validate(ctx, identity, credential, "initialize", digest)
}

func (a *SignedControllerAuthority) validate(ctx context.Context, identity ControllerIdentity, credential, purpose, digest string) (ControllerGrant, error) {
	if err := ctx.Err(); err != nil {
		return ControllerGrant{}, err
	}
	denied := errors.New("executor: signed controller admission denied")
	parts := strings.Split(credential, ".")
	if len(credential) > 8192 || len(parts) != 2 {
		return ControllerGrant{}, denied
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ControllerGrant{}, denied
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !ed25519.Verify(a.config.VerificationKey, append([]byte(SignedControllerCapability+"\x00"), data...), signature) {
		return ControllerGrant{}, denied
	}
	var admission ControllerAdmission
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&admission) != nil || !admission.valid() || admission.Identity != identity || identity.ResourceID != a.config.ResourceID || identity.Incarnation != a.config.Incarnation || admission.Purpose != purpose || admission.SHA256 != digest {
		return ControllerGrant{}, denied
	}
	return admission.Grant, nil
}
