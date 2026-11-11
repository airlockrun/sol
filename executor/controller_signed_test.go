package executor

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSignedControllerAdmissionScopeAndTamper(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, 32))
	identity := ControllerIdentity{ResourceID: "resource", Incarnation: "incarnation", BootID: "boot"}
	config := ControllerServerConfig{ResourceID: identity.ResourceID, Incarnation: identity.Incarnation, VerificationKey: key.Public().(ed25519.PublicKey), ArtifactToken: "artifact-only", BootstrapSHA256: strings.Repeat("a", 64)}
	authority, err := NewSignedControllerAuthority(config)
	if err != nil {
		t.Fatal(err)
	}
	admission := ControllerAdmission{Protocol: SignedControllerCapability, Identity: identity, Grant: ControllerGrant{Epoch: 1, AdmissionID: "admission", ValidUntil: time.Now().Add(time.Minute)}, Run: "run", Task: "task", Owner: "owner", Purpose: "execute", IssuedAt: time.Now().Add(-time.Second)}
	credential, err := SignControllerAdmission(key, admission)
	if err != nil {
		t.Fatal(err)
	}
	if grant, err := authority.Validate(t.Context(), identity, credential); err != nil || !sameAdmission(grant, admission.Grant) || !grant.ValidUntil.Equal(admission.Grant.ValidUntil) {
		t.Fatal(grant, err)
	}
	for _, field := range []string{"resource", "incarnation", "boot", "epoch", "admission", "owner", "run", "task", "purpose", "digest", "expiry", "protocol"} {
		t.Run(field, func(t *testing.T) {
			bad := admission
			switch field {
			case "resource":
				bad.Identity.ResourceID += "x"
			case "incarnation":
				bad.Identity.Incarnation += "x"
			case "boot":
				bad.Identity.BootID += "x"
			case "epoch":
				bad.Grant.Epoch++
			case "admission":
				bad.Grant.AdmissionID += "x"
			case "owner":
				bad.Owner += "x"
			case "run":
				bad.Run += "x"
			case "task":
				bad.Task += "x"
			case "purpose":
				bad.Purpose = "initialize"
			case "digest":
				bad.SHA256 = config.BootstrapSHA256
			case "expiry":
				bad.Grant.ValidUntil = time.Now().Add(time.Hour)
			case "protocol":
				bad.Protocol += "x"
			}
			data, _ := json.Marshal(bad)
			tampered := base64.RawURLEncoding.EncodeToString(data) + "." + strings.Split(credential, ".")[1]
			if _, err := authority.Validate(t.Context(), identity, tampered); err == nil {
				t.Fatal("tampered claim accepted")
			}
		})
	}
	for _, field := range []string{"resource", "incarnation", "boot", "purpose", "digest", "expired", "future", "unbounded", "key", "artifact"} {
		t.Run("signed_"+field, func(t *testing.T) {
			bad, signingKey := admission, key
			switch field {
			case "resource":
				bad.Identity.ResourceID += "x"
			case "incarnation":
				bad.Identity.Incarnation += "x"
			case "boot":
				bad.Identity.BootID += "x"
			case "purpose":
				bad.Purpose = "initialize"
				bad.SHA256 = config.BootstrapSHA256
			case "digest":
				bad.Purpose = "initialize"
				bad.SHA256 = strings.Repeat("b", 64)
			case "expired":
				bad.Grant.ValidUntil = time.Now().Add(-time.Second)
			case "future":
				bad.IssuedAt = time.Now().Add(time.Second)
			case "unbounded":
				bad.Grant.ValidUntil = bad.IssuedAt.Add(ControllerAdmissionLifetime + time.Second)
			case "key":
				signingKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{2}, 32))
			}
			// Sign raw invalid timestamps too: verification, not just the issuer,
			// must enforce the bounded window.
			data, _ := json.Marshal(bad)
			signature := ed25519.Sign(signingKey, append([]byte(SignedControllerCapability+"\x00"), data...))
			value := base64.RawURLEncoding.EncodeToString(data) + "." + base64.RawURLEncoding.EncodeToString(signature)
			if field == "artifact" {
				value = config.ArtifactToken
			}
			if _, err := authority.Validate(t.Context(), identity, value); err == nil {
				t.Fatal("unscoped execution accepted")
			}
			if _, err := authority.ValidateBootstrap(t.Context(), identity, value, config.BootstrapSHA256); err == nil && field != "purpose" {
				t.Fatal("unscoped initialization accepted")
			}
		})
	}
	admission.Purpose, admission.SHA256 = "initialize", config.BootstrapSHA256
	credential, err = SignControllerAdmission(key, admission)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ValidateBootstrap(t.Context(), identity, credential, config.BootstrapSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Validate(t.Context(), identity, credential); err == nil {
		t.Fatal("bootstrap admitted execution")
	}
}

func TestSignedControllerDelayedAdmissionOrdersOnlyDeliveredGenerations(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32))
	authority, err := NewSignedControllerAuthority(ControllerServerConfig{ResourceID: "r", Incarnation: "i", VerificationKey: key.Public().(ed25519.PublicKey), ArtifactToken: "artifact"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewControlledToolServer(&controllerTestExecutor{}, "r", "i", authority)
	sign := func(epoch uint64) string {
		value, err := SignControllerAdmission(key, ControllerAdmission{Protocol: SignedControllerCapability, Identity: server.ControllerIdentity(), Grant: ControllerGrant{Epoch: epoch, AdmissionID: "admission", ValidUntil: time.Now().Add(time.Minute)}, Run: "run", Owner: "owner", Purpose: "execute", IssuedAt: time.Now().Add(-time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	delayed, newer := sign(2), sign(3)
	// The issuer can already have generated epoch 3. Until delivered, this
	// verifier cannot distinguish delayed epoch 2 from a current admission.
	conn, err := server.controller.admit(t.Context(), delayed)
	if err != nil {
		t.Fatal(err)
	}
	conn.cancel()
	close(conn.done)
	conn, err = server.controller.admit(t.Context(), newer)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.cancel()
	defer close(conn.done)
	if _, err := server.controller.admit(t.Context(), delayed); err == nil {
		t.Fatal("delivered high-water allowed stale admission")
	}
	if _, err := server.controller.admit(t.Context(), newer); err == nil {
		t.Fatal("equal admission reused")
	}
}

func TestSignedControllerExpiresWhileDrainingAndConsumesGeneration(t *testing.T) {
	key := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	authority, err := NewSignedControllerAuthority(ControllerServerConfig{ResourceID: "r", Incarnation: "i", VerificationKey: key.Public().(ed25519.PublicKey), ArtifactToken: "artifact"})
	if err != nil {
		t.Fatal(err)
	}
	server := NewControlledToolServer(&controllerTestExecutor{}, "r", "i", authority)
	sign := func(epoch uint64, until time.Time) string {
		value, err := SignControllerAdmission(key, ControllerAdmission{Protocol: SignedControllerCapability, Identity: server.ControllerIdentity(), Grant: ControllerGrant{Epoch: epoch, AdmissionID: "admission", ValidUntil: until}, Run: "run", Owner: "owner", Purpose: "execute", IssuedAt: time.Now().Add(-time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	old, err := server.controller.admit(t.Context(), sign(1, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	defer old.cancel()
	until := time.Now().Add(150 * time.Millisecond)
	ticket := sign(2, until)
	done := make(chan error, 1)
	go func() { _, err := server.controller.admit(t.Context(), ticket); done <- err }()
	select {
	case <-old.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("successor did not cancel predecessor")
	}
	// Cancellation is not foreground settlement: withhold the predecessor's
	// joined-done signal until the successor admission has expired.
	<-time.After(time.Until(until) + time.Millisecond)
	close(old.done)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expired successor became READY")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("admission did not settle")
	}
	server.controller.mu.Lock()
	high, active := server.controller.high.Epoch, server.controller.active
	server.controller.mu.Unlock()
	if high != 2 || active != nil {
		t.Fatal("expired draining admission was not consumed", high, active)
	}
	if _, err := server.controller.admit(t.Context(), sign(2, time.Now().Add(time.Minute))); err == nil {
		t.Fatal("equal generation revived consumed admission")
	}
	next, err := server.controller.admit(t.Context(), sign(3, time.Now().Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	next.cancel()
	close(next.done)
}
