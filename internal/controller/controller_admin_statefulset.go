package controller

import (
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
)

// adminCredentialsMountPath is the in-pod directory where the admin
// CredentialsSecret is projected by createAdminStatefulSet; each key in
// the secret becomes a file named after the key.
const adminCredentialsMountPath = "/etc/sw/admin"

// adminCredentialKeys are the well-known keys in the admin CredentialsSecret
// that weed admin accepts as `-<key>=<value>` flags. adminUser/adminPassword
// enable the login flow; readOnlyUser/readOnlyPassword are optional view-only
// accounts. We pass these as flags rather than env vars because older weed
// builds do not bind WEED_ADMIN_* env vars to the admin command (see #239).
var adminCredentialKeys = []string{"adminUser", "adminPassword", "readOnlyUser", "readOnlyPassword"}

func buildAdminStartupScript(m *seaweedv1.Seaweed, extraArgs ...string) string {
	commands := []string{"weed", "-logtostderr=true"}
	if arg := tlsConfigDirArg(m); arg != "" {
		commands = append(commands, arg)
	}
	commands = append(commands, "admin")
	commands = append(commands, fmt.Sprintf("-port=%d", seaweedv1.AdminHTTPPort))
	commands = append(commands, fmt.Sprintf("-master=%s", getMasterPeersString(m)))
	if m.Spec.Admin.MetricsPort != nil {
		commands = append(commands, fmt.Sprintf("-metricsPort=%d", *m.Spec.Admin.MetricsPort))
	}
	commands = append(commands, extraArgs...)

	weedCmd := strings.Join(commands, " ")

	// When a CredentialsSecret is mounted, resolve each well-known key from
	// the projected files into `-<key>=<value>` flags at container start, so
	// weed admin boots with authentication enabled. Keys with no file on disk
	// are skipped, keeping readOnlyUser/readOnlyPassword optional. `set --`
	// builds positional parameters so values containing spaces or special
	// characters expand safely through `"$@"`. `exec` replaces the /bin/sh
	// wrapper with weed so SIGTERM from the kubelet reaches weed directly.
	if m.Spec.Admin.CredentialsSecret != nil && m.Spec.Admin.CredentialsSecret.Name != "" {
		preamble := "set --; " +
			"for key in " + strings.Join(adminCredentialKeys, " ") + "; do " +
			`f="` + adminCredentialsMountPath + `/$key"; ` +
			`[ -f "$f" ] && set -- "$@" "-$key=$(cat "$f")"; ` +
			"done; "
		return preamble + "exec " + weedCmd + ` "$@"`
	}

	return "exec " + weedCmd
}

// adminURLPrefix scans weed admin ExtraArgs for a -urlPrefix flag and returns
// its value normalized to a leading slash with no trailing slash. The admin
// server mounts every route — including `/health` and `/metrics` — behind
// `http.StripPrefix(urlPrefix, r)` when this flag is set (see upstream
// weed/command/admin.go), so any k8s probes or ServiceMonitor endpoints must
// target the prefixed path or they will 404.
//
// Parsing follows the same semantics as upstream's fla9 parser (a clone of
// Go's `flag` package): a string flag always consumes the next arg as its
// value when the `=` form is not used, even if that next arg looks like
// another flag. Only a bare flag at the very end of the slice has no value.
// Returns "" when no prefix is configured.
func adminURLPrefix(extraArgs []string) string {
	const flag = "urlPrefix"
	var (
		raw   string
		found bool
	)
	for i := 0; i < len(extraArgs); i++ {
		a := extraArgs[i]
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "--"), "-"), "=")
		if name != flag {
			continue
		}
		if hasValue {
			raw, found = value, true
			continue
		}
		if i+1 < len(extraArgs) {
			i++
			raw, found = extraArgs[i], true
			continue
		}
		raw, found = "", true
	}
	if !found {
		return ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "/") {
		raw = "/" + raw
	}
	return strings.TrimRight(raw, "/")
}

// adminRoutePath returns `route` prefixed with any `-urlPrefix` supplied via
// the admin ExtraArgs. `route` must already start with a `/`.
// See issue #204.
func adminRoutePath(extraArgs []string, route string) string {
	return adminURLPrefix(extraArgs) + route
}

func (r *SeaweedReconciler) createAdminStatefulSet(m *seaweedv1.Seaweed) *appsv1.StatefulSet {
	labels := labelsForAdmin(m.Name)
	annotations := m.BaseAdminSpec().Annotations()
	ports := []corev1.ContainerPort{
		{
			ContainerPort: seaweedv1.AdminHTTPPort,
			Name:          "admin-http",
		},
		{
			ContainerPort: seaweedv1.AdminGRPCPort,
			Name:          "admin-grpc",
		},
	}
	if m.Spec.Admin.MetricsPort != nil {
		ports = append(ports, corev1.ContainerPort{
			ContainerPort: *m.Spec.Admin.MetricsPort,
			Name:          "admin-metrics",
		})
	}
	replicas := int32(1)
	rollingUpdatePartition := int32(0)
	enableServiceLinks := false

	adminPodSpec := m.BaseAdminSpec().BuildPodSpec()

	var volumeMounts []corev1.VolumeMount
	if tlsVols, tlsMounts := tlsVolumesAndMounts(m); len(tlsVols) > 0 {
		adminPodSpec.Volumes = append(adminPodSpec.Volumes, tlsVols...)
		volumeMounts = append(volumeMounts, tlsMounts...)
	}

	extraArgs := m.BaseAdminSpec().ExtraArgs()
	healthPath := adminRoutePath(extraArgs, "/health")

	// Project the configured CredentialsSecret as a read-only volume; the
	// startup script in buildAdminStartupScript reads each well-known key
	// from this directory and forwards it to weed admin as a flag.
	if m.Spec.Admin.CredentialsSecret != nil && m.Spec.Admin.CredentialsSecret.Name != "" {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      "admin-credentials",
			ReadOnly:  true,
			MountPath: adminCredentialsMountPath,
		})
		adminPodSpec.Volumes = append(adminPodSpec.Volumes, corev1.Volume{
			Name: "admin-credentials",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: m.Spec.Admin.CredentialsSecret.Name,
				},
			},
		})
	}

	env := append(m.BaseAdminSpec().Env(), kubernetesEnvVars...)

	adminPodSpec.EnableServiceLinks = &enableServiceLinks
	adminPodSpec.Containers = []corev1.Container{{
		Name:            "admin",
		Image:           m.Spec.Image,
		ImagePullPolicy: m.BaseAdminSpec().ImagePullPolicy(),
		Env:             env,
		Resources:       filterContainerResources(m.Spec.Admin.ResourceRequirements),
		VolumeMounts:    mergeVolumeMounts(volumeMounts, m.BaseAdminSpec().VolumeMounts()),
		Command: []string{
			"/bin/sh",
			"-ec",
			buildAdminStartupScript(m, extraArgs...),
		},
		Ports: ports,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   healthPath,
					Port:   intstr.FromInt(seaweedv1.AdminHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 10,
			TimeoutSeconds:      3,
			PeriodSeconds:       15,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path:   healthPath,
					Port:   intstr.FromInt(seaweedv1.AdminHTTPPort),
					Scheme: corev1.URISchemeHTTP,
				},
			},
			InitialDelaySeconds: 20,
			TimeoutSeconds:      3,
			PeriodSeconds:       30,
			SuccessThreshold:    1,
			FailureThreshold:    6,
		},
	}}
	adminPodSpec.Containers = append(adminPodSpec.Containers, m.BaseAdminSpec().Sidecars()...)

	dep := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name + "-admin",
			Namespace: m.Namespace,
		},
		Spec: appsv1.StatefulSetSpec{
			ServiceName:         m.Name + "-admin-peer",
			PodManagementPolicy: appsv1.ParallelPodManagement,
			Replicas:            &replicas,
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: appsv1.RollingUpdateStatefulSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateStatefulSetStrategy{
					Partition: &rollingUpdatePartition,
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      labels,
					Annotations: annotations,
				},
				Spec: adminPodSpec,
			},
		},
	}
	return dep
}
