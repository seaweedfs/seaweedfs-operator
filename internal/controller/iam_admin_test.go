/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/seaweedfs/seaweedfs-operator/internal/controller/swadmin"
)

func TestMapIAMError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil", nil, nil},
		{"notfound", status.Error(codes.NotFound, "user x not found"), ErrIAMNotFound},
		{"alreadyexists", status.Error(codes.AlreadyExists, "user x already exists"), ErrIAMUserAlreadyExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapIAMError(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMapIAMError_PassesThroughOther(t *testing.T) {
	in := status.Error(codes.Internal, "boom")
	if got := mapIAMError(in); !errors.Is(got, in) {
		t.Fatalf("expected internal error to pass through, got %v", got)
	}
}

// fakeIAMAdmin is a stateful in-memory IAMAdmin for controller tests. It
// mirrors the idempotency contract of swadminIAMAdmin: lookups return
// ErrIAMNotFound when absent; DeleteUser/DeleteAccessKey/DeletePolicy are
// idempotent; AttachPolicy/DetachPolicy require the user to exist.
type fakeIAMAdmin struct {
	mu         sync.Mutex
	users      map[string]*swadmin.IAMUser
	policies   map[string]string
	secretKeys map[string]string // accessKey -> secretKey, to assert generation/adoption
	providers  map[string]string // issuerURL -> arn
	calls      []string

	createUserErr   error
	createAKErr     error
	putPolicyErr    error
	attachPolicyErr error
	putOIDCErr      error
}

func newFakeIAMAdmin() *fakeIAMAdmin {
	return &fakeIAMAdmin{
		users:      map[string]*swadmin.IAMUser{},
		policies:   map[string]string{},
		secretKeys: map[string]string{},
		providers:  map[string]string{},
	}
}

func (f *fakeIAMAdmin) seedUser(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[name] = &swadmin.IAMUser{Name: name}
}

func (f *fakeIAMAdmin) record(c string) { f.calls = append(f.calls, c) }

func (f *fakeIAMAdmin) GetUser(_ context.Context, name string) (*swadmin.IAMUser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[name]
	if !ok {
		return nil, ErrIAMNotFound
	}
	cp := *u
	cp.AccessKeys = append([]string(nil), u.AccessKeys...)
	cp.PolicyNames = append([]string(nil), u.PolicyNames...)
	return &cp, nil
}

func (f *fakeIAMAdmin) CreateUser(_ context.Context, name, displayName, email string, disabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateUser:" + name)
	if f.createUserErr != nil {
		return f.createUserErr
	}
	if _, ok := f.users[name]; ok {
		return ErrIAMUserAlreadyExists
	}
	f.users[name] = &swadmin.IAMUser{Name: name, DisplayName: displayName, Email: email, Disabled: disabled}
	return nil
}

func (f *fakeIAMAdmin) SetUserState(_ context.Context, name, displayName, email string, disabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("SetUserState:" + name)
	u, ok := f.users[name]
	if !ok {
		return ErrIAMNotFound
	}
	u.DisplayName = displayName
	u.Email = email
	u.Disabled = disabled
	return nil
}

func (f *fakeIAMAdmin) DeleteUser(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DeleteUser:" + name)
	delete(f.users, name)
	return nil
}

func (f *fakeIAMAdmin) CreateAccessKey(_ context.Context, user, accessKey, secretKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CreateAccessKey:" + user + ":" + accessKey)
	if f.createAKErr != nil {
		return f.createAKErr
	}
	u, ok := f.users[user]
	if !ok {
		return ErrIAMNotFound
	}
	for _, existing := range u.AccessKeys {
		if existing == accessKey {
			// Mirror the IAM service, which rejects a duplicate access key.
			return errors.New("access key already exists")
		}
	}
	u.AccessKeys = append(u.AccessKeys, accessKey)
	f.secretKeys[accessKey] = secretKey
	return nil
}

func (f *fakeIAMAdmin) DeleteAccessKey(_ context.Context, user, accessKey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DeleteAccessKey:" + user + ":" + accessKey)
	u, ok := f.users[user]
	if !ok {
		return nil
	}
	kept := u.AccessKeys[:0]
	for _, ak := range u.AccessKeys {
		if ak != accessKey {
			kept = append(kept, ak)
		}
	}
	u.AccessKeys = kept
	delete(f.secretKeys, accessKey)
	return nil
}

func (f *fakeIAMAdmin) PutPolicy(_ context.Context, name, document string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PutPolicy:" + name)
	if f.putPolicyErr != nil {
		return f.putPolicyErr
	}
	f.policies[name] = document
	return nil
}

func (f *fakeIAMAdmin) GetPolicy(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	doc, ok := f.policies[name]
	if !ok {
		return "", ErrIAMNotFound
	}
	return doc, nil
}

func (f *fakeIAMAdmin) DeletePolicy(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DeletePolicy:" + name)
	delete(f.policies, name)
	return nil
}

func (f *fakeIAMAdmin) AttachPolicy(_ context.Context, user, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("AttachPolicy:" + user + ":" + policy)
	if f.attachPolicyErr != nil {
		return f.attachPolicyErr
	}
	u, ok := f.users[user]
	if !ok {
		return ErrIAMNotFound
	}
	for _, p := range u.PolicyNames {
		if p == policy {
			return nil
		}
	}
	u.PolicyNames = append(u.PolicyNames, policy)
	return nil
}

func (f *fakeIAMAdmin) DetachPolicy(_ context.Context, user, policy string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DetachPolicy:" + user + ":" + policy)
	u, ok := f.users[user]
	if !ok {
		return ErrIAMNotFound
	}
	kept := u.PolicyNames[:0]
	for _, p := range u.PolicyNames {
		if p != policy {
			kept = append(kept, p)
		}
	}
	u.PolicyNames = kept
	return nil
}

func (f *fakeIAMAdmin) userPolicies(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[name]
	if !ok {
		return nil
	}
	return append([]string(nil), u.PolicyNames...)
}

func (f *fakeIAMAdmin) userKeys(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[name]
	if !ok {
		return nil
	}
	return append([]string(nil), u.AccessKeys...)
}

func (f *fakeIAMAdmin) PutOIDCProvider(_ context.Context, provider swadmin.OIDCProvider) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("PutOIDCProvider:" + provider.IssuerURL)
	if f.putOIDCErr != nil {
		return "", f.putOIDCErr
	}
	arn := "arn:aws:iam::seaweedfs:oidc-provider/" + provider.IssuerURL
	f.providers[provider.IssuerURL] = arn
	return arn, nil
}

func (f *fakeIAMAdmin) DeleteOIDCProvider(_ context.Context, issuerURL string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("DeleteOIDCProvider:" + issuerURL)
	delete(f.providers, issuerURL) // idempotent
	return nil
}

// compile-time check that the fake satisfies the interface.
var _ IAMAdmin = (*fakeIAMAdmin)(nil)
