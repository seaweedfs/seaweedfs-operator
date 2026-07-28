package v1

import "strings"

// applyVersion returns image with its tag replaced by version.
//
// A digest-pinned image is returned untouched: a digest is an exact,
// tamper-evident pin, and silently swapping it for a mutable tag would
// weaken a supply-chain guarantee the user deliberately opted into.
//
// The registry host may itself carry a port (registry:5000/seaweedfs), so the
// tag separator is only looked for in the final path segment.
func applyVersion(image, version string) string {
	if version == "" || image == "" {
		return image
	}
	name := image
	if slash := strings.LastIndex(image, "/"); slash >= 0 {
		name = image[slash+1:]
	}
	if strings.Contains(name, "@") {
		return image
	}
	if colon := strings.LastIndex(name, ":"); colon >= 0 {
		image = image[:len(image)-len(name)+colon]
	}
	return image + ":" + version
}
