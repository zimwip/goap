// Command iam manages organizations, users and roles. Skeleton: the API
// answers Unimplemented until milestone M2.
package main

import (
	"github.com/zimwip/goap/gen/goap/iam/v1/iamv1connect"
	"github.com/zimwip/goap/internal/platform"
)

func main() {
	log := platform.Logger("iam")
	srv := platform.NewServer(log, platform.Env("GOAP_HTTP_ADDR", ":8080"))
	srv.Mount(iamv1connect.NewIamServiceHandler(iamv1connect.UnimplementedIamServiceHandler{}))
	if err := srv.Run(); err != nil {
		platform.Fatal(log, "server", err)
	}
}
