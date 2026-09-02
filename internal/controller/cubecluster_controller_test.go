package controller

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/akurbanov/cube-microk8s-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAPIAndRefreshDeploymentsUseCubeContracts(t *testing.T) {
	cluster := testCluster()
	r := &CubeClusterReconciler{}

	api := r.apiDeployment(cluster)
	if got := api.Spec.Template.Spec.Containers[0].Image; got != defaultCubeImage || !strings.Contains(got, "@sha256:") {
		t.Fatalf("API image = %q, want pinned default %q", got, defaultCubeImage)
	}
	container := api.Spec.Template.Spec.Containers[0]
	if got := container.ReadinessProbe.HTTPGet.Path; got != "/readyz" {
		t.Errorf("readiness path = %q, want /readyz", got)
	}
	if got := container.LivenessProbe.HTTPGet.Path; got != "/livez" {
		t.Errorf("liveness path = %q, want /livez", got)
	}
	if got := envValue(container.Env, "CUBEJS_CUBESTORE_HOST"); got != "example-cubestore" {
		t.Errorf("Cube Store host = %q, want example-cubestore", got)
	}
	if got := api.Spec.Template.Spec.InitContainers[0].Command[2]; got != "cp -L /model-source/* /cube-model/" {
		t.Errorf("model loader command = %q", got)
	}

	refresh := r.refreshDeployment(cluster)
	if got := envValue(refresh.Spec.Template.Spec.Containers[0].Env, "CUBEJS_REFRESH_WORKER"); got != "true" {
		t.Errorf("CUBEJS_REFRESH_WORKER = %q, want true", got)
	}
}

func TestClusteredCubeStoreTopology(t *testing.T) {
	cluster := testCluster()
	cluster.Spec.CubeStore.Mode = "clustered"
	cluster.Spec.CubeStore.Workers = ptr(int32(3))
	cluster.Spec.CubeStore.Scratch.Size = "2Gi"
	r := &CubeClusterReconciler{}

	router := r.routerStatefulSet(cluster)
	workers := r.workerStatefulSet(cluster)
	wantWorkers := "example-cubestore-worker-0.example-cubestore-worker:9001," +
		"example-cubestore-worker-1.example-cubestore-worker:9001," +
		"example-cubestore-worker-2.example-cubestore-worker:9001"
	if got := envValue(router.Spec.Template.Spec.Containers[0].Env, "CUBESTORE_WORKERS"); got != wantWorkers {
		t.Errorf("router workers = %q, want %q", got, wantWorkers)
	}
	if got := *workers.Spec.Replicas; got != 3 {
		t.Errorf("worker replicas = %d, want 3", got)
	}
	if got := workers.Spec.Template.Spec.Containers[0].ReadinessProbe.TCPSocket.Port.StrVal; got != "worker" {
		t.Errorf("worker readiness probe port = %q, want worker", got)
	}
	limit := workers.Spec.Template.Spec.Volumes[0].EmptyDir.SizeLimit
	if limit == nil || limit.String() != "2Gi" {
		t.Errorf("scratch size limit = %v, want 2Gi", limit)
	}
}

func TestSingleCubeStoreUsesSelectedRemoteStorage(t *testing.T) {
	cluster := testCluster()
	cluster.Spec.CubeStore.RemoteStorage = platformv1alpha1.RemoteStorageSpec{
		Type: "s3",
		S3:   &platformv1alpha1.S3Storage{Bucket: "cube", Region: "us-west-2"},
	}
	container := (&CubeClusterReconciler{}).singleStoreDeployment(cluster).Spec.Template.Spec.Containers[0]
	if got := envValue(container.Env, "CUBESTORE_S3_BUCKET"); got != "cube" {
		t.Errorf("CUBESTORE_S3_BUCKET = %q, want cube", got)
	}
	if got := envValue(container.Env, "CUBESTORE_REMOTE_DIR"); got != "" {
		t.Errorf("CUBESTORE_REMOTE_DIR = %q for S3 storage, want unset", got)
	}
}

func TestValidateReferencesChecksS3SecretAndReplicaCounts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := testCluster()
	cluster.Spec.CubeStore.RemoteStorage = platformv1alpha1.RemoteStorageSpec{
		Type: "s3",
		S3: &platformv1alpha1.S3Storage{
			Bucket: "cube", Region: "us-west-2", SecretRef: "s3-credentials",
		},
	}
	r := &CubeClusterReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "test"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "configuration", Namespace: "test"}},
	).Build()}
	if err := r.validateReferences(context.Background(), cluster); err == nil || !strings.Contains(err.Error(), "S3 credentials secret") {
		t.Fatalf("validateReferences error = %v, want missing S3 secret", err)
	}

	r.Client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "model", Namespace: "test"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "configuration", Namespace: "test"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "s3-credentials", Namespace: "test"}},
	).Build()
	cluster.Spec.API.Replicas = ptr(int32(0))
	if err := r.validateReferences(context.Background(), cluster); err == nil || !strings.Contains(err.Error(), "replicas") {
		t.Fatalf("validateReferences error = %v, want invalid replica count", err)
	}
}

func TestWorkloadsAvailableRequiresEveryComponent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cluster := testCluster()
	objects := []runtime.Object{
		readyDeployment("example-api", 1),
		readyDeployment("example-refresh-worker", 1),
		readyDeployment("example-cubestore", 0),
	}
	r := &CubeClusterReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()}
	readyAPI, available, message, err := r.workloadsAvailable(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if readyAPI != 1 || available || !strings.Contains(message, "Cube Store 0/1") {
		t.Fatalf("got readyAPI=%d available=%t message=%q", readyAPI, available, message)
	}
}

func TestCopyIntoPreservesAllocatedServiceFields(t *testing.T) {
	family := corev1.IPFamilyPolicySingleStack
	current := &corev1.Service{Spec: corev1.ServiceSpec{
		ClusterIP: "10.0.0.1", ClusterIPs: []string{"10.0.0.1"},
		IPFamilies: []corev1.IPFamily{corev1.IPv4Protocol}, IPFamilyPolicy: &family,
		Ports: []corev1.ServicePort{{Name: "http", Port: 4000, NodePort: 30123}},
	}}
	desired := &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 4000}}}}
	copyInto(current, desired)
	if current.Spec.ClusterIP != "10.0.0.1" || current.Spec.Ports[0].NodePort != 30123 {
		t.Fatalf("allocated service fields were not preserved: %#v", current.Spec)
	}
}

func testCluster() *platformv1alpha1.CubeCluster {
	return &platformv1alpha1.CubeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "test"},
		Spec: platformv1alpha1.CubeClusterSpec{
			ModelConfigMap: "model", ConfigurationSecret: "configuration",
			API:           platformv1alpha1.WorkloadSpec{Replicas: ptr(int32(1))},
			RefreshWorker: platformv1alpha1.WorkloadSpec{Replicas: ptr(int32(1))},
			CubeStore: platformv1alpha1.CubeStoreSpec{
				Mode: "single",
				RemoteStorage: platformv1alpha1.RemoteStorageSpec{
					Type:             "persistentVolume",
					PersistentVolume: &platformv1alpha1.PersistentVolumeStorage{ExistingClaim: "remote"},
				},
			},
		},
	}
}

func readyDeployment(name string, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: ready},
	}
}

func envValue(environment []corev1.EnvVar, name string) string {
	for _, variable := range environment {
		if variable.Name == name {
			return variable.Value
		}
	}
	return ""
}
