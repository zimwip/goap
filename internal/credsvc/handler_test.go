package credsvc_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	credentialsv1 "github.com/zimwip/goap/gen/goap/credentials/v1"
	"github.com/zimwip/goap/gen/goap/credentials/v1/credentialsv1connect"
	"github.com/zimwip/goap/internal/credsvc"
)

// Only the gateway, presenting the service credential, may call the credentials service: without it nobody can
// verify a password, set one or end sessions; and a service with no credential configured refuses everybody.
func TestCredentialsNeedTheServiceToken(t *testing.T) {
	ctx := context.Background()
	svc := &credsvc.Service{Store: credsvc.NewMemoryStore()}
	serve := func(token string) *httptest.Server {
		mux := http.NewServeMux()
		mux.Handle(credentialsv1connect.NewCredentialsServiceHandler(&credsvc.Handler{Service: svc, Token: token}))
		return httptest.NewServer(mux)
	}
	srv := serve("s3cret")
	defer srv.Close()
	anon := credentialsv1connect.NewCredentialsServiceClient(srv.Client(), srv.URL)
	gateway := credentialsv1connect.NewCredentialsServiceClient(srv.Client(), srv.URL, credsvc.ClientToken("s3cret"))
	wrong := credentialsv1connect.NewCredentialsServiceClient(srv.Client(), srv.URL, credsvc.ClientToken("nope"))

	if _, err := gateway.Register(ctx, connect.NewRequest(&credentialsv1.RegisterRequest{Subject: "alice", Password: "correct horse"})); err != nil {
		t.Fatalf("gateway register: %v", err)
	}
	for name, cl := range map[string]credentialsv1connect.CredentialsServiceClient{"no token": anon, "wrong token": wrong} {
		if _, err := cl.SetPassword(ctx, connect.NewRequest(&credentialsv1.SetPasswordRequest{Subject: "alice", Password: "hijacked"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: SetPassword = %v", name, err)
		}
		if _, err := cl.EndSessions(ctx, connect.NewRequest(&credentialsv1.EndSessionsRequest{Subject: "alice"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: EndSessions = %v", name, err)
		}
		if _, err := cl.Verify(ctx, connect.NewRequest(&credentialsv1.VerifyRequest{Subject: "alice", Password: "correct horse"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
			t.Errorf("%s: Verify = %v", name, err)
		}
	}
	if ok, err := svc.Verify(ctx, "alice", "correct horse"); err != nil || !ok {
		t.Fatalf("the password changed through a refused call: %v %v", ok, err)
	}
	if r, err := gateway.Verify(ctx, connect.NewRequest(&credentialsv1.VerifyRequest{Subject: "alice", Password: "correct horse"})); err != nil || !r.Msg.GetOk() {
		t.Fatalf("gateway verify: %v %v", r, err)
	}

	// no token configured: nobody gets in, not even with an empty header
	open := serve("")
	defer open.Close()
	cl := credentialsv1connect.NewCredentialsServiceClient(open.Client(), open.URL)
	if _, err := cl.Exists(ctx, connect.NewRequest(&credentialsv1.ExistsRequest{Subject: "alice"})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("no token configured: %v", err)
	}
}
