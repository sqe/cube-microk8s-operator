package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/akurbanov/cube-microk8s-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	defaultCubeImage      = "cubejs/cube:v1.7.20@sha256:7f14f4be9f3303afe48a16584480c8a9dc15f44c13daf66c2a5b31019025b71a"
	defaultCubeStoreImage = "cubejs/cubestore:v1.7.20@sha256:cd5fe68049204640704a6412a39e7a09eb391fc70890577dd21b5480d85cb219"
)

type CubeClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=platform.cube.dev,resources=cubeclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.cube.dev,resources=cubeclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.cube.dev,resources=cubeclusters/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;persistentvolumeclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=apps,resources=deployments;statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=policy,resources=poddisruptionbudgets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

func (r *CubeClusterReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	cluster := &platformv1alpha1.CubeCluster{}
	if err := r.Get(ctx, request.NamespacedName, cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if err := r.validateReferences(ctx, cluster); err != nil {
		if statusErr := r.setStatus(ctx, cluster, metav1.ConditionFalse, "InvalidConfiguration", err.Error(), 0); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	for _, object := range r.objects(cluster) {
		if err := r.reconcileObject(ctx, cluster, object); err != nil {
			if statusErr := r.setStatus(ctx, cluster, metav1.ConditionFalse, "ReconcileFailed", err.Error(), 0); statusErr != nil {
				return ctrl.Result{}, fmt.Errorf("reconcile object: %w (update status: %v)", err, statusErr)
			}
			return ctrl.Result{}, err
		}
	}
	if err := r.removeObsoleteStore(ctx, cluster); err != nil {
		return ctrl.Result{}, err
	}

	ready, available, message, err := r.workloadsAvailable(ctx, cluster)
	if err != nil {
		return ctrl.Result{}, err
	}
	condition := metav1.ConditionFalse
	reason := "Progressing"
	if available {
		condition, reason, message = metav1.ConditionTrue, "Available", "Cube API, refresh worker, and Cube Store are ready"
	}
	if err := r.setStatus(ctx, cluster, condition, reason, message, ready); err != nil {
		return ctrl.Result{}, err
	}
	if condition == metav1.ConditionFalse {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *CubeClusterReconciler) workloadsAvailable(ctx context.Context, cluster *platformv1alpha1.CubeCluster) (int32, bool, string, error) {
	namespacedName := func(component string) types.NamespacedName {
		return types.NamespacedName{Name: cluster.Name + "-" + component, Namespace: cluster.Namespace}
	}
	api := &appsv1.Deployment{}
	if err := r.Get(ctx, namespacedName("api"), api); err != nil {
		return 0, false, "", err
	}
	apiExpected := replicas(cluster.Spec.API.Replicas, 2)
	parts := []string{fmt.Sprintf("API %d/%d", api.Status.ReadyReplicas, apiExpected)}
	available := api.Status.ReadyReplicas >= apiExpected

	refresh := &appsv1.Deployment{}
	if err := r.Get(ctx, namespacedName("refresh-worker"), refresh); err != nil {
		return api.Status.ReadyReplicas, false, "", err
	}
	refreshExpected := replicas(cluster.Spec.RefreshWorker.Replicas, 1)
	parts = append(parts, fmt.Sprintf("refresh %d/%d", refresh.Status.ReadyReplicas, refreshExpected))
	available = available && refresh.Status.ReadyReplicas >= refreshExpected

	if storeMode(cluster) == "clustered" {
		router := &appsv1.StatefulSet{}
		if err := r.Get(ctx, namespacedName("cubestore-router"), router); err != nil {
			return api.Status.ReadyReplicas, false, "", err
		}
		workers := &appsv1.StatefulSet{}
		if err := r.Get(ctx, namespacedName("cubestore-worker"), workers); err != nil {
			return api.Status.ReadyReplicas, false, "", err
		}
		workerExpected := replicas(cluster.Spec.CubeStore.Workers, 2)
		parts = append(parts, fmt.Sprintf("Cube Store router %d/1, workers %d/%d", router.Status.ReadyReplicas, workers.Status.ReadyReplicas, workerExpected))
		available = available && router.Status.ReadyReplicas >= 1 && workers.Status.ReadyReplicas >= workerExpected
	} else {
		store := &appsv1.Deployment{}
		if err := r.Get(ctx, namespacedName("cubestore"), store); err != nil {
			return api.Status.ReadyReplicas, false, "", err
		}
		parts = append(parts, fmt.Sprintf("Cube Store %d/1", store.Status.ReadyReplicas))
		available = available && store.Status.ReadyReplicas >= 1
	}

	return api.Status.ReadyReplicas, available, strings.Join(parts, "; "), nil
}

func (r *CubeClusterReconciler) validateReferences(ctx context.Context, cluster *platformv1alpha1.CubeCluster) error {
	if cluster.Spec.ModelConfigMap == "" || cluster.Spec.ConfigurationSecret == "" {
		return fmt.Errorf("spec.modelConfigMap and spec.configurationSecret are required")
	}
	for _, object := range []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}} {
		name := cluster.Spec.ModelConfigMap
		if _, ok := object.(*corev1.Secret); ok {
			name = cluster.Spec.ConfigurationSecret
		}
		if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: cluster.Namespace}, object); err != nil {
			return fmt.Errorf("required %T %q: %w", object, name, err)
		}
	}
	storage := cluster.Spec.CubeStore.RemoteStorage
	if replicas(cluster.Spec.API.Replicas, 2) < 1 || replicas(cluster.Spec.RefreshWorker.Replicas, 1) < 1 {
		return fmt.Errorf("API and refresh worker replicas must be at least 1")
	}
	if storeMode(cluster) == "clustered" && replicas(cluster.Spec.CubeStore.Workers, 2) < 1 {
		return fmt.Errorf("clustered Cube Store workers must be at least 1")
	}
	if cluster.Spec.CubeStore.Mode != "" && cluster.Spec.CubeStore.Mode != "single" && cluster.Spec.CubeStore.Mode != "clustered" {
		return fmt.Errorf("unsupported Cube Store mode %q", cluster.Spec.CubeStore.Mode)
	}
	if cluster.Spec.CubeStore.Scratch.Size != "" {
		if _, err := resource.ParseQuantity(cluster.Spec.CubeStore.Scratch.Size); err != nil {
			return fmt.Errorf("invalid Cube Store scratch size: %w", err)
		}
	}
	if cluster.Spec.Service.NodePort != 0 && cluster.Spec.Service.Type != corev1.ServiceTypeNodePort && cluster.Spec.Service.Type != corev1.ServiceTypeLoadBalancer {
		return fmt.Errorf("spec.service.nodePort requires service type NodePort or LoadBalancer")
	}
	switch storage.Type {
	case "persistentVolume":
		if storage.PersistentVolume == nil || storage.PersistentVolume.ExistingClaim == "" {
			return fmt.Errorf("persistentVolume storage requires existingClaim")
		}
		if err := r.Get(ctx, types.NamespacedName{Name: storage.PersistentVolume.ExistingClaim, Namespace: cluster.Namespace}, &corev1.PersistentVolumeClaim{}); err != nil {
			return fmt.Errorf("remote storage claim: %w", err)
		}
	case "s3":
		if storage.S3 == nil || storage.S3.Bucket == "" || storage.S3.Region == "" {
			return fmt.Errorf("s3 storage requires bucket and region")
		}
		if storage.S3.SecretRef != "" {
			if err := r.Get(ctx, types.NamespacedName{Name: storage.S3.SecretRef, Namespace: cluster.Namespace}, &corev1.Secret{}); err != nil {
				return fmt.Errorf("S3 credentials secret: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported remote storage type %q", storage.Type)
	}
	return nil
}

func (r *CubeClusterReconciler) reconcileObject(ctx context.Context, owner *platformv1alpha1.CubeCluster, desired client.Object) error {
	current := desired.DeepCopyObject().(client.Object)
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, current, func() error {
		resourceVersion := current.GetResourceVersion()
		copyInto(current, desired)
		current.SetResourceVersion(resourceVersion)
		return controllerutil.SetControllerReference(owner, current, r.Scheme)
	})
	return err
}

func copyInto(dst, src client.Object) {
	switch d := dst.(type) {
	case *appsv1.Deployment:
		*d = *src.(*appsv1.Deployment).DeepCopy()
	case *appsv1.StatefulSet:
		*d = *src.(*appsv1.StatefulSet).DeepCopy()
	case *corev1.Service:
		clusterIP, clusterIPs := d.Spec.ClusterIP, d.Spec.ClusterIPs
		ipFamilies, ipFamilyPolicy := d.Spec.IPFamilies, d.Spec.IPFamilyPolicy
		healthCheckNodePort := d.Spec.HealthCheckNodePort
		allocatedNodePorts := map[string]int32{}
		for _, port := range d.Spec.Ports {
			allocatedNodePorts[port.Name] = port.NodePort
		}
		*d = *src.(*corev1.Service).DeepCopy()
		d.Spec.ClusterIP, d.Spec.ClusterIPs = clusterIP, clusterIPs
		d.Spec.IPFamilies, d.Spec.IPFamilyPolicy = ipFamilies, ipFamilyPolicy
		d.Spec.HealthCheckNodePort = healthCheckNodePort
		for i := range d.Spec.Ports {
			if d.Spec.Ports[i].NodePort == 0 {
				d.Spec.Ports[i].NodePort = allocatedNodePorts[d.Spec.Ports[i].Name]
			}
		}
	case *policyv1.PodDisruptionBudget:
		*d = *src.(*policyv1.PodDisruptionBudget).DeepCopy()
	case *networkingv1.NetworkPolicy:
		*d = *src.(*networkingv1.NetworkPolicy).DeepCopy()
	}
}

func (r *CubeClusterReconciler) objects(cluster *platformv1alpha1.CubeCluster) []client.Object {
	objects := []client.Object{r.apiService(cluster), r.apiDeployment(cluster), r.refreshDeployment(cluster), r.apiPDB(cluster), r.networkPolicy(cluster)}
	if storeMode(cluster) == "clustered" {
		objects = append(objects, r.routerService(cluster), r.workerService(cluster), r.routerStatefulSet(cluster), r.workerStatefulSet(cluster))
	} else {
		objects = append(objects, r.singleStoreService(cluster), r.singleStoreDeployment(cluster))
	}
	return objects
}

func (r *CubeClusterReconciler) apiService(c *platformv1alpha1.CubeCluster) *corev1.Service {
	typeValue := c.Spec.Service.Type
	if typeValue == "" {
		typeValue = corev1.ServiceTypeClusterIP
	}
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-api", Namespace: c.Namespace, Labels: labels(c, "api"), Annotations: c.Spec.Service.Annotations}, Spec: corev1.ServiceSpec{Type: typeValue, Selector: labels(c, "api"), Ports: []corev1.ServicePort{{Name: "http", Port: 4000, NodePort: c.Spec.Service.NodePort}}}}
}

func (r *CubeClusterReconciler) apiDeployment(c *platformv1alpha1.CubeCluster) *appsv1.Deployment {
	count := replicas(c.Spec.API.Replicas, 2)
	return deployment(c, "api", count, cubeContainer(c, "api", c.Spec.API.Resources, nil))
}

func (r *CubeClusterReconciler) refreshDeployment(c *platformv1alpha1.CubeCluster) *appsv1.Deployment {
	count := replicas(c.Spec.RefreshWorker.Replicas, 1)
	return deployment(c, "refresh-worker", count, cubeContainer(c, "refresh-worker", c.Spec.RefreshWorker.Resources, []corev1.EnvVar{{Name: "CUBEJS_REFRESH_WORKER", Value: "true"}}))
}

func deployment(c *platformv1alpha1.CubeCluster, component string, count int32, container corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-" + component, Namespace: c.Namespace, Labels: labels(c, component)}, Spec: appsv1.DeploymentSpec{Replicas: &count, Selector: &metav1.LabelSelector{MatchLabels: labels(c, component)}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c, component)}, Spec: corev1.PodSpec{SecurityContext: podSecurityContext(), InitContainers: []corev1.Container{modelLoader(c)}, Containers: []corev1.Container{container}, Volumes: modelVolumes(c)}}}}
}

func modelLoader(c *platformv1alpha1.CubeCluster) corev1.Container {
	image := c.Spec.Image
	if image == "" {
		image = defaultCubeImage
	}
	return corev1.Container{Name: "model-loader", Image: image, ImagePullPolicy: pullPolicy(c), Command: []string{"sh", "-c", "cp -L /model-source/* /cube-model/"}, SecurityContext: containerSecurityContext(), VolumeMounts: []corev1.VolumeMount{{Name: "model-source", MountPath: "/model-source", ReadOnly: true}, {Name: "model", MountPath: "/cube-model"}}}
}

func cubeContainer(c *platformv1alpha1.CubeCluster, component string, resources corev1.ResourceRequirements, extra []corev1.EnvVar) corev1.Container {
	image := c.Spec.Image
	if image == "" {
		image = defaultCubeImage
	}
	env := []corev1.EnvVar{{Name: "CUBEJS_DEV_MODE", Value: "false"}, {Name: "CUBEJS_CUBESTORE_HOST", Value: cubeStoreHost(c)}, {Name: "CUBEJS_TELEMETRY", Value: "false"}}
	env = append(env, extra...)
	return corev1.Container{Name: component, Image: image, ImagePullPolicy: pullPolicy(c), Env: env, EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: c.Spec.ConfigurationSecret}}}}, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 4000}}, Resources: resources, SecurityContext: containerSecurityContext(), VolumeMounts: []corev1.VolumeMount{{Name: "model", MountPath: "/cube/conf/model", ReadOnly: true}}, ReadinessProbe: httpProbe("/readyz"), LivenessProbe: httpProbe("/livez")}
}

func modelVolumes(c *platformv1alpha1.CubeCluster) []corev1.Volume {
	return []corev1.Volume{{Name: "model-source", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: c.Spec.ModelConfigMap}}}}, {Name: "model", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
}

func httpProbe(path string) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromString("http")}}, InitialDelaySeconds: 10, PeriodSeconds: 10, TimeoutSeconds: 5, FailureThreshold: 6}
}

func (r *CubeClusterReconciler) singleStoreService(c *platformv1alpha1.CubeCluster) *corev1.Service {
	return storeService(c, "cubestore", labels(c, "cubestore"), false, []corev1.ServicePort{{Name: "http", Port: 3030}})
}

func (r *CubeClusterReconciler) singleStoreDeployment(c *platformv1alpha1.CubeCluster) *appsv1.Deployment {
	container, volumes := storeContainer(c, "cubestore", nil)
	return &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore", Namespace: c.Namespace, Labels: labels(c, "cubestore")}, Spec: appsv1.DeploymentSpec{Replicas: ptr(int32(1)), Selector: &metav1.LabelSelector{MatchLabels: labels(c, "cubestore")}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c, "cubestore")}, Spec: corev1.PodSpec{SecurityContext: podSecurityContext(), Containers: []corev1.Container{container}, Volumes: volumes}}}}
}

func (r *CubeClusterReconciler) routerService(c *platformv1alpha1.CubeCluster) *corev1.Service {
	return storeService(c, "cubestore-router", labels(c, "cubestore-router"), false, []corev1.ServicePort{{Name: "http", Port: 3030}, {Name: "meta", Port: 9999}})
}

func (r *CubeClusterReconciler) workerService(c *platformv1alpha1.CubeCluster) *corev1.Service {
	return storeService(c, "cubestore-worker", labels(c, "cubestore-worker"), true, []corev1.ServicePort{{Name: "worker", Port: 9001}})
}

func storeService(c *platformv1alpha1.CubeCluster, component string, selector map[string]string, headless bool, ports []corev1.ServicePort) *corev1.Service {
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-" + component, Namespace: c.Namespace, Labels: selector}, Spec: corev1.ServiceSpec{Selector: selector, Ports: ports}}
	if headless {
		service.Spec.ClusterIP = corev1.ClusterIPNone
	}
	return service
}

func (r *CubeClusterReconciler) routerStatefulSet(c *platformv1alpha1.CubeCluster) *appsv1.StatefulSet {
	workers := workerAddresses(c)
	env := []corev1.EnvVar{{Name: "CUBESTORE_SERVER_NAME", Value: c.Name + "-cubestore-router:9999"}, {Name: "CUBESTORE_META_PORT", Value: "9999"}, {Name: "CUBESTORE_WORKERS", Value: workers}}
	container, volumes := storeContainer(c, "router", env)
	return stateful(c, "cubestore-router", 1, container, volumes)
}

func (r *CubeClusterReconciler) workerStatefulSet(c *platformv1alpha1.CubeCluster) *appsv1.StatefulSet {
	count := replicas(c.Spec.CubeStore.Workers, 2)
	env := []corev1.EnvVar{{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}}, {Name: "CUBESTORE_SERVER_NAME", Value: "$(POD_NAME)." + c.Name + "-cubestore-worker:9001"}, {Name: "CUBESTORE_WORKER_PORT", Value: "9001"}, {Name: "CUBESTORE_META_ADDR", Value: c.Name + "-cubestore-router:9999"}, {Name: "CUBESTORE_WORKERS", Value: workerAddresses(c)}}
	container, volumes := storeContainer(c, "worker", env)
	return stateful(c, "cubestore-worker", count, container, volumes)
}

func stateful(c *platformv1alpha1.CubeCluster, component string, count int32, container corev1.Container, volumes []corev1.Volume) *appsv1.StatefulSet {
	return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-" + component, Namespace: c.Namespace, Labels: labels(c, component)}, Spec: appsv1.StatefulSetSpec{ServiceName: c.Name + "-" + component, Replicas: &count, PodManagementPolicy: appsv1.ParallelPodManagement, Selector: &metav1.LabelSelector{MatchLabels: labels(c, component)}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c, component)}, Spec: corev1.PodSpec{SecurityContext: podSecurityContext(), Containers: []corev1.Container{container}, Volumes: volumes}}}}
}

func storeContainer(c *platformv1alpha1.CubeCluster, name string, roleEnv []corev1.EnvVar) (corev1.Container, []corev1.Volume) {
	image := c.Spec.CubeStore.Image
	if image == "" {
		image = defaultCubeStoreImage
	}
	env, envFrom, volumes, mounts := storageConfiguration(c)
	env = append(env, roleEnv...)
	probePort := "http"
	if name == "worker" {
		probePort = "worker"
	}
	container := corev1.Container{Name: name, Image: image, ImagePullPolicy: pullPolicy(c), Env: env, EnvFrom: envFrom, Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 3030}, {Name: "meta", ContainerPort: 9999}, {Name: "worker", ContainerPort: 9001}}, Resources: c.Spec.CubeStore.Resources, SecurityContext: containerSecurityContext(), VolumeMounts: mounts, ReadinessProbe: tcpProbe(probePort), LivenessProbe: tcpProbe(probePort)}
	return container, volumes
}

func storageConfiguration(c *platformv1alpha1.CubeCluster) ([]corev1.EnvVar, []corev1.EnvFromSource, []corev1.Volume, []corev1.VolumeMount) {
	env := []corev1.EnvVar{}
	envFrom := []corev1.EnvFromSource{}
	emptyDir := &corev1.EmptyDirVolumeSource{}
	if c.Spec.CubeStore.Scratch.Size != "" {
		size := resource.MustParse(c.Spec.CubeStore.Scratch.Size)
		emptyDir.SizeLimit = &size
	}
	volumes := []corev1.Volume{{Name: "scratch", VolumeSource: corev1.VolumeSource{EmptyDir: emptyDir}}}
	mounts := []corev1.VolumeMount{{Name: "scratch", MountPath: "/cube/data"}}
	storage := c.Spec.CubeStore.RemoteStorage
	if storage.Type == "s3" {
		env = append(env, corev1.EnvVar{Name: "CUBESTORE_S3_BUCKET", Value: storage.S3.Bucket}, corev1.EnvVar{Name: "CUBESTORE_S3_REGION", Value: storage.S3.Region})
		if storage.S3.SecretRef != "" {
			envFrom = append(envFrom, corev1.EnvFromSource{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: storage.S3.SecretRef}}})
		}
	} else {
		volumes = append(volumes, corev1.Volume{Name: "remote", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: storage.PersistentVolume.ExistingClaim}}})
		mounts = append(mounts, corev1.VolumeMount{Name: "remote", MountPath: "/cube/remote"})
		env = append(env, corev1.EnvVar{Name: "CUBESTORE_REMOTE_DIR", Value: "/cube/remote"})
	}
	return env, envFrom, volumes, mounts
}

func (r *CubeClusterReconciler) apiPDB(c *platformv1alpha1.CubeCluster) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-api", Namespace: c.Namespace}, Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &intstr.IntOrString{Type: intstr.Int, IntVal: 1}, Selector: &metav1.LabelSelector{MatchLabels: labels(c, "api")}}}
}

func (r *CubeClusterReconciler) networkPolicy(c *platformv1alpha1.CubeCluster) *networkingv1.NetworkPolicy {
	allPorts := []networkingv1.NetworkPolicyPort{{Port: intOrString(3030)}, {Port: intOrString(9001)}, {Port: intOrString(9999)}}
	return &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore", Namespace: c.Namespace}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/instance": c.Name, "platform.cube.dev/store": "true"}}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/instance": c.Name}}}}, Ports: allPorts}}}}
}

func (r *CubeClusterReconciler) removeObsoleteStore(ctx context.Context, c *platformv1alpha1.CubeCluster) error {
	var objects []client.Object
	if storeMode(c) == "clustered" {
		objects = []client.Object{&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore", Namespace: c.Namespace}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore", Namespace: c.Namespace}}}
	} else {
		objects = []client.Object{&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore-router", Namespace: c.Namespace}}, &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore-worker", Namespace: c.Namespace}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore-router", Namespace: c.Namespace}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-cubestore-worker", Namespace: c.Namespace}}}
	}
	for _, object := range objects {
		if err := r.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *CubeClusterReconciler) setStatus(ctx context.Context, c *platformv1alpha1.CubeCluster, status metav1.ConditionStatus, reason, message string, ready int32) error {
	base := c.DeepCopy()
	c.Status.ObservedGeneration = c.Generation
	c.Status.ReadyAPIs = ready
	c.Status.Endpoint = fmt.Sprintf("http://%s-api.%s.svc:4000", c.Name, c.Namespace)
	apimeta.SetStatusCondition(&c.Status.Conditions, metav1.Condition{Type: "Ready", Status: status, Reason: reason, Message: message, ObservedGeneration: c.Generation})
	if err := r.Status().Patch(ctx, c, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("update CubeCluster status: %w", err)
	}
	return nil
}

func (r *CubeClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.CubeCluster{}).
		Owns(&appsv1.Deployment{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&policyv1.PodDisruptionBudget{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Complete(r)
}

func labels(c *platformv1alpha1.CubeCluster, component string) map[string]string {
	result := map[string]string{"app.kubernetes.io/name": "cube", "app.kubernetes.io/instance": c.Name, "app.kubernetes.io/component": component, "app.kubernetes.io/part-of": "cube"}
	if strings.HasPrefix(component, "cubestore") {
		result["platform.cube.dev/store"] = "true"
	}
	return result
}
func replicas(value *int32, fallback int32) int32 {
	if value != nil {
		return *value
	}
	return fallback
}
func storeMode(c *platformv1alpha1.CubeCluster) string {
	if c.Spec.CubeStore.Mode == "clustered" {
		return "clustered"
	}
	return "single"
}
func cubeStoreHost(c *platformv1alpha1.CubeCluster) string {
	if storeMode(c) == "clustered" {
		return c.Name + "-cubestore-router"
	}
	return c.Name + "-cubestore"
}
func workerAddresses(c *platformv1alpha1.CubeCluster) string {
	count := replicas(c.Spec.CubeStore.Workers, 2)
	addresses := make([]string, count)
	for i := range count {
		addresses[i] = fmt.Sprintf("%s-cubestore-worker-%d.%s-cubestore-worker:9001", c.Name, i, c.Name)
	}
	return strings.Join(addresses, ",")
}
func pullPolicy(c *platformv1alpha1.CubeCluster) corev1.PullPolicy {
	if c.Spec.ImagePullPolicy != "" {
		return c.Spec.ImagePullPolicy
	}
	return corev1.PullIfNotPresent
}
func ptr[T any](value T) *T                      { return &value }
func intOrString(port int32) *intstr.IntOrString { value := intstr.FromInt32(port); return &value }
func tcpProbe(port string) *corev1.Probe {
	return &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromString(port)}}, InitialDelaySeconds: 5, PeriodSeconds: 10, FailureThreshold: 6}
}
func podSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}
}
func containerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
}
