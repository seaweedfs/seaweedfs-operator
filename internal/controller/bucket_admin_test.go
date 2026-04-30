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
	"errors"
	"testing"
)

func TestIsBucketNotFoundErr(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":              {nil, false},
		"unrelated":        {errors.New("connection refused"), false},
		"lookup bucket":    {errors.New("lookup bucket photos: not found"), true},
		"did not find":     {errors.New("did not find bucket photos: rpc error"), true},
		"bucket not found": {errors.New("bucket not found: lookup error"), true},
		"plain not found":  {errors.New("entry not found"), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isBucketNotFoundErr(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsAlreadyExistsErr(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":            {nil, false},
		"already exists": {errors.New("entry already exists"), true},
		"file exists":    {errors.New("file exists"), true},
		"unrelated":      {errors.New("permission denied"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isAlreadyExistsErr(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsRetentionErr(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":             {nil, false},
		"retention":       {errors.New("bucket has objects with active Object Lock retention or legal hold"), true},
		"legal hold only": {errors.New("bucket has legal hold objects"), true},
		"unrelated":       {errors.New("rpc error"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isRetentionErr(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsObjectLockSuspendErr(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":            {nil, false},
		"happy path":     {errors.New("cannot suspend versioning on bucket photos: Object Lock is enabled"), true},
		"missing prefix": {errors.New("Object Lock is enabled"), false},
		"missing suffix": {errors.New("cannot suspend versioning on bucket photos: blocked"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isObjectLockSuspendErr(tc.err); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
