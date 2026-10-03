package main

import (
	"strings"
	"testing"
)

// baseConfig is the minimum a reserve job needs, so parseConfig gets past its
// own job_queue/job_file check and we can isolate the auth behaviour.
const baseConfig = `"job_queue":"megatron","ssh_keys":["gh:talhaHavadar"]`

func TestParseConfigAuthFromEnv(t *testing.T) {
	t.Setenv("TF_CLIENT", "cid-123")
	t.Setenv("TF_SECRET", "sek-456")

	cfg, err := parseConfig([]byte(`{` + baseConfig + `,"client_id_env":"TF_CLIENT","secret_key_env":"TF_SECRET"}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ClientID != "cid-123" || cfg.SecretKey != "sek-456" {
		t.Errorf("env not resolved: client_id=%q secret_key=%q", cfg.ClientID, cfg.SecretKey)
	}
}

func TestParseConfigAuthLiteral(t *testing.T) {
	cfg, err := parseConfig([]byte(`{` + baseConfig + `,"client_id":"cid","secret_key":"sek"}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ClientID != "cid" || cfg.SecretKey != "sek" {
		t.Errorf("literal not kept: client_id=%q secret_key=%q", cfg.ClientID, cfg.SecretKey)
	}
}

func TestParseConfigAuthEnvOverridesLiteral(t *testing.T) {
	t.Setenv("TF_SECRET", "from-env")
	cfg, err := parseConfig([]byte(`{` + baseConfig + `,"client_id":"cid","secret_key":"from-config","secret_key_env":"TF_SECRET"}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.SecretKey != "from-env" {
		t.Errorf("env should override literal, got %q", cfg.SecretKey)
	}
}

func TestParseConfigAuthHalfSetRejected(t *testing.T) {
	cases := map[string]string{
		"only client_id": `,"client_id":"cid"`,
		"only secret_key": `,"secret_key":"sek"`,
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseConfig([]byte(`{` + baseConfig + extra + `}`))
			if err == nil || !strings.Contains(err.Error(), "must be set together") {
				t.Errorf("expected both-or-neither error, got: %v", err)
			}
		})
	}
}

func TestParseConfigAuthUnsetEnvDoesNotHalfSet(t *testing.T) {
	// client_id_env naming an unset var leaves client_id empty; with no
	// secret_key either, that is the valid "no auth" case, not an error.
	cfg, err := parseConfig([]byte(`{` + baseConfig + `,"client_id_env":"TF_DEFINITELY_UNSET"}`))
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.ClientID != "" || cfg.SecretKey != "" {
		t.Errorf("expected no auth, got client_id=%q secret_key=%q", cfg.ClientID, cfg.SecretKey)
	}
}

func TestTfCmdInjectsAuthEnv(t *testing.T) {
	cfg := config{TestflingerBin: "testflinger", ClientID: "cid", SecretKey: "sek"}
	cmd := tfCmd(cfg, "status", "some-job")
	if cmd.Env["TESTFLINGER_CLIENT_ID"] != "cid" || cmd.Env["TESTFLINGER_SECRET_KEY"] != "sek" {
		t.Errorf("auth env not injected: %v", cmd.Env)
	}

	// withTimeout must preserve the injected env.
	if got := withTimeout(tfCmd(cfg, "jobs"), 10).Env["TESTFLINGER_SECRET_KEY"]; got != "sek" {
		t.Errorf("withTimeout dropped auth env, got %q", got)
	}
}

func TestTfCmdNoAuthWhenUnset(t *testing.T) {
	cmd := tfCmd(config{TestflingerBin: "testflinger"}, "status", "some-job")
	if cmd.Env != nil {
		t.Errorf("expected nil env with no credentials, got %v", cmd.Env)
	}
}
