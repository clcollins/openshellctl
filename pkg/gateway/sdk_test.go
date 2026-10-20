package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/fake"
	types "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
)

func newFakeGateway(opts ...fake.ClientOption) *client {
	fc := fake.NewClient(opts...)
	return &client{conn: &Conn{SDK: fc}}
}

func TestSDK_CreateGetList(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()

	sb, err := c.CreateSandbox(ctx, "default", "sb-1", &types.SandboxSpec{}, map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if sb.Status.Phase != types.SandboxProvisioning {
		t.Errorf("phase = %v, want Provisioning", sb.Status.Phase)
	}

	got, err := c.GetSandbox(ctx, "default", "sb-1")
	if err != nil {
		t.Fatalf("GetSandbox: %v", err)
	}
	if got.Name != "sb-1" {
		t.Errorf("name = %q", got.Name)
	}

	list, err := c.ListSandboxes(ctx, "default", types.ListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("list len = %d, want 1", len(list))
	}
}

func TestSDK_GetNotFoundClassified(t *testing.T) {
	c := newFakeGateway()
	_, err := c.GetSandbox(context.Background(), "default", "ghost")
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
}

func TestSDK_CreateAlreadyExistsClassified(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()
	if _, err := c.CreateSandbox(ctx, "default", "dup", &types.SandboxSpec{}, nil); err != nil {
		t.Fatal(err)
	}
	_, err := c.CreateSandbox(ctx, "default", "dup", &types.SandboxSpec{}, nil)
	var ae *AlreadyExistsError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want AlreadyExistsError", err)
	}
}

func TestSDK_StopStart(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()
	if _, err := c.CreateSandbox(ctx, "default", "sb", &types.SandboxSpec{}, nil); err != nil {
		t.Fatal(err)
	}
	stopped, err := c.StopSandbox(ctx, "default", "sb")
	if err != nil {
		t.Fatalf("StopSandbox: %v", err)
	}
	if stopped.Status.Phase != types.SandboxStopped {
		t.Errorf("phase = %v, want Stopped", stopped.Status.Phase)
	}
	started, err := c.StartSandbox(ctx, "default", "sb")
	if err != nil {
		t.Fatalf("StartSandbox: %v", err)
	}
	if started.Status.Phase != types.SandboxReady {
		t.Errorf("phase = %v, want Ready", started.Status.Phase)
	}
}

func TestSDK_CurrentUser(t *testing.T) {
	want := &types.CurrentUser{Subject: "user-1", Roles: []string{"openshell-user"}}
	c := newFakeGateway(fake.WithCurrentUser(want))
	got, err := c.CurrentUser(context.Background())
	if err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if got.Subject != "user-1" || len(got.Roles) != 1 {
		t.Errorf("current user = %+v", got)
	}
}

func TestSDK_ListProviders(t *testing.T) {
	c := newFakeGateway()
	got, err := c.ListProviders(context.Background(), "default", types.ListOptions{})
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("providers = %d, want 0", len(got))
	}
}

func TestSDK_ProviderCreateGetUpdateDelete(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()

	created, err := c.CreateProvider(ctx, "default", &types.Provider{
		Name: "p1",
		Type: "github",
		Spec: types.ProviderSpec{Credentials: map[string]string{"TOKEN": "abc"}},
	})
	if err != nil {
		t.Fatalf("CreateProvider: %v", err)
	}
	if created.Name != "p1" || created.Type != "github" {
		t.Errorf("created = %+v", created)
	}

	got, err := c.GetProvider(ctx, "default", "p1")
	if err != nil {
		t.Fatalf("GetProvider: %v", err)
	}
	if got.Spec.Credentials["TOKEN"] != "abc" {
		t.Errorf("got.Spec.Credentials = %+v", got.Spec.Credentials)
	}

	got.Spec.Credentials["TOKEN"] = "def"
	updated, err := c.UpdateProvider(ctx, "default", got)
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.Spec.Credentials["TOKEN"] != "def" {
		t.Errorf("updated.Spec.Credentials = %+v", updated.Spec.Credentials)
	}

	if err := c.DeleteProvider(ctx, "default", "p1"); err != nil {
		t.Fatalf("DeleteProvider: %v", err)
	}
	if _, err := c.GetProvider(ctx, "default", "p1"); err == nil {
		t.Fatal("GetProvider after delete: expected error")
	}
}

func TestSDK_GetProviderNotFoundClassified(t *testing.T) {
	c := newFakeGateway()
	_, err := c.GetProvider(context.Background(), "default", "ghost")
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
}

func TestSDK_CreateProviderAlreadyExistsClassified(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()
	if _, err := c.CreateProvider(ctx, "default", &types.Provider{Name: "dup", Type: "github"}); err != nil {
		t.Fatal(err)
	}
	_, err := c.CreateProvider(ctx, "default", &types.Provider{Name: "dup", Type: "github"})
	var ae *AlreadyExistsError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v, want AlreadyExistsError", err)
	}
}

func TestSDK_UpdateProviderNotFoundClassified(t *testing.T) {
	c := newFakeGateway()
	_, err := c.UpdateProvider(context.Background(), "default", &types.Provider{Name: "ghost", Type: "github"})
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
}

// TestSDK_DeleteProviderMissing verifies that deleting a missing provider
// returns a NotFoundError (0.1.x SDK changed from idempotent to NotFound).
func TestSDK_DeleteProviderMissing(t *testing.T) {
	c := newFakeGateway()
	err := c.DeleteProvider(context.Background(), "default", "ghost")
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("err = %v, want NotFoundError", err)
	}
}

// TestSDK_CreateUpdateProviderNilGuard exercises the `if provider != nil`
// name-extraction branch in CreateProvider/UpdateProvider's error path: a nil
// provider must still classify cleanly (empty name), not panic.
func TestSDK_CreateUpdateProviderNilGuard(t *testing.T) {
	c := newFakeGateway()
	ctx := context.Background()

	_, err := c.CreateProvider(ctx, "default", nil)
	var inv *InvalidArgumentError
	if !errors.As(err, &inv) {
		t.Fatalf("CreateProvider(nil) err = %v, want InvalidArgumentError", err)
	}

	_, err = c.UpdateProvider(ctx, "default", nil)
	if !errors.As(err, &inv) {
		t.Fatalf("UpdateProvider(nil) err = %v, want InvalidArgumentError", err)
	}
}

// New wires a Gateway from a Conn; assert it returns a usable value.
func TestNew_ReturnsGateway(t *testing.T) {
	fc := fake.NewClient()
	gw := New(&Conn{SDK: fc}, nil)
	if gw == nil {
		t.Fatal("New returned nil")
	}
}
