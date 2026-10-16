package sandbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// KubernetesProvisioner starts one pod per sandbox through the Kubernetes
// API (in-cluster service account). Pods run non-root with a read-only root
// file system, no service account token, dropped capabilities, the
// RuntimeDefault seccomp profile and optional RuntimeClass (gVisor, Kata).
// A NetworkPolicy (deploy/k8s/sandbox-networkpolicy.yaml) must restrict
// their egress to the engine.
type KubernetesProvisioner struct {
	// APIServer, Token and CA default to the in-cluster configuration.
	APIServer    string
	Token        string
	CAFile       string
	Namespace    string
	RuntimeClass string
	// HTTP overrides the API client (tests).
	HTTP *http.Client
}

var _ Provisioner = (*KubernetesProvisioner)(nil)

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

func (k *KubernetesProvisioner) Name() string { return "kubernetes" }

func (k *KubernetesProvisioner) init() error {
	if k.APIServer == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" {
			return fmt.Errorf("kubernetes provisioner: not running in a cluster and no API server configured")
		}
		k.APIServer = "https://" + host + ":" + port
	}
	if k.Token == "" {
		b, err := os.ReadFile(saDir + "/token")
		if err != nil {
			return err
		}
		k.Token = strings.TrimSpace(string(b))
	}
	if k.Namespace == "" {
		if b, err := os.ReadFile(saDir + "/namespace"); err == nil {
			k.Namespace = strings.TrimSpace(string(b))
		} else {
			k.Namespace = "default"
		}
	}
	if k.HTTP == nil {
		caFile := k.CAFile
		if caFile == "" {
			caFile = saDir + "/ca.crt"
		}
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return err
		}
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(pem)
		k.HTTP = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	}
	return nil
}

func (k *KubernetesProvisioner) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	if err := k.init(); err != nil {
		return 0, err
	}
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(k.APIServer, "/")+path, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if k.Token != "" {
		req.Header.Set("Authorization", "Bearer "+k.Token)
	}
	resp, err := k.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("kubernetes %s %s: %s: %s", method, path, resp.Status, bytes.TrimSpace(raw))
	}
	if out != nil {
		return resp.StatusCode, json.Unmarshal(raw, out)
	}
	return resp.StatusCode, nil
}

// Start implements Provisioner: create the pod and wait for its IP.
func (k *KubernetesProvisioner) Start(ctx context.Context, spec Spec) (Instance, error) {
	if spec.Image == "" {
		return Instance{}, fmt.Errorf("kubernetes provisioner: sandbox image not configured")
	}
	limits := map[string]string{}
	if spec.MemoryMB > 0 {
		limits["memory"] = fmt.Sprintf("%dMi", spec.MemoryMB)
	}
	if spec.CPUs > 0 {
		limits["cpu"] = fmt.Sprintf("%dm", int64(spec.CPUs*1000))
	}
	podSpec := map[string]any{
		"restartPolicy":                "Never",
		"automountServiceAccountToken": false,
		"enableServiceLinks":           false,
		"securityContext": map[string]any{
			"runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
			"seccompProfile": map[string]string{"type": "RuntimeDefault"},
		},
		"containers": []any{map[string]any{
			"name":      "runner",
			"image":     spec.Image,
			"env":       []any{map[string]string{"name": "GOAP_HTTP_ADDR", "value": ":8080"}, map[string]string{"name": "GOAP_SANDBOX_ID", "value": spec.ID}},
			"ports":     []any{map[string]int{"containerPort": 8080}},
			"resources": map[string]any{"limits": limits},
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
				"capabilities": map[string]any{"drop": []string{"ALL"}},
			},
			"volumeMounts": []any{map[string]string{"name": "tmp", "mountPath": "/tmp"}},
		}},
		"volumes": []any{map[string]any{"name": "tmp", "emptyDir": map[string]string{"medium": "Memory", "sizeLimit": "64Mi"}}},
	}
	if k.RuntimeClass != "" {
		podSpec["runtimeClassName"] = k.RuntimeClass
	}
	pod := map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"name": spec.ID, "labels": map[string]string{"app.kubernetes.io/name": "goap-sandbox", "goap.io/process": spec.ProcessID}},
		"spec":     podSpec,
	}
	if _, err := k.do(ctx, http.MethodPost, "/api/v1/namespaces/"+k.Namespace+"/pods", pod, nil); err != nil {
		return Instance{}, err
	}
	for {
		var got struct {
			Status struct {
				Phase string `json:"phase"`
				PodIP string `json:"podIP"`
			} `json:"status"`
		}
		if _, err := k.do(ctx, http.MethodGet, "/api/v1/namespaces/"+k.Namespace+"/pods/"+spec.ID, nil, &got); err != nil {
			return Instance{}, err
		}
		switch {
		case got.Status.Phase == "Running" && got.Status.PodIP != "":
			return Instance{ID: spec.ID, Endpoint: "http://" + got.Status.PodIP + ":8080"}, nil
		case got.Status.Phase == "Failed" || got.Status.Phase == "Succeeded":
			_ = k.Stop(context.Background(), spec.ID)
			return Instance{}, fmt.Errorf("sandbox pod %s ended (%s)", spec.ID, got.Status.Phase)
		}
		select {
		case <-ctx.Done():
			_ = k.Stop(context.Background(), spec.ID)
			return Instance{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// Stop implements Provisioner.
func (k *KubernetesProvisioner) Stop(ctx context.Context, id string) error {
	code, err := k.do(ctx, http.MethodDelete, "/api/v1/namespaces/"+k.Namespace+"/pods/"+id+"?gracePeriodSeconds=0", nil, nil)
	if code == http.StatusNotFound {
		return nil
	}
	return err
}
