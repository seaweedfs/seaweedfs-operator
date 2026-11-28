package v1

import "strings"

// ImageRepository returns image without its tag or digest, with Docker Hub
// names qualified, so only references to the same repository compare equal:
// "weed:3.6" and "docker.io/library/weed@sha256:…" are both
// "docker.io/library/weed".
//
// It backs the workload-identity check shared by the AdminScript controller and
// its admission webhook: a script may change the tag or digest of the image it
// runs, but not the repository the cluster admin chose.
func ImageRepository(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		image = image[:i]
	}
	domain, path := "docker.io", image
	if i := strings.Index(image, "/"); i >= 0 && (strings.ContainsAny(image[:i], ".:") || image[:i] == "localhost") {
		domain, path = image[:i], image[i+1:]
	}
	if domain == "index.docker.io" {
		domain = "docker.io"
	}
	if domain == "docker.io" && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	return domain + "/" + path
}

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
