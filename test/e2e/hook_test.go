package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/argoproj/gitops-engine/pkg/health"
	. "github.com/argoproj/gitops-engine/pkg/sync/common"

	. "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	. "github.com/argoproj/argo-cd/v3/test/e2e/fixture"
	. "github.com/argoproj/argo-cd/v3/test/e2e/fixture/app"
	"github.com/argoproj/argo-cd/v3/util/lua"
)

func TestPreSyncHookSuccessful(t *testing.T) {
	// special-case that the pod remains in the running state, but we don't really care, because this is only used for
	// determining overall operation status is a sync with >1 wave/phase
	testHookSuccessful(t, HookTypePreSync)
}

func TestSyncHookSuccessful(t *testing.T) {
	testHookSuccessful(t, HookTypeSync)
}

func TestPostSyncHookSuccessful(t *testing.T) {
	testHookSuccessful(t, HookTypePostSync)
}

// make sure we can run a standard sync hook
func testHookSuccessful(t *testing.T, hookType HookType) {
	t.Helper()
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", fmt.Sprintf(`[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": %q}}]`, hookType)).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(ResourceSyncStatusIs("Pod", "pod", SyncStatusCodeSynced)).
		Expect(ResourceHealthIs("Pod", "pod", health.HealthStatusHealthy)).
		Expect(ResourceResultNumbering(2)).
		Expect(ResourceResultIs(ResourceResult{Version: "v1", Kind: "Pod", Namespace: DeploymentNamespace(), Images: []string{"quay.io/argoprojlabs/argocd-e2e-container:0.1"}, Name: "hook", Message: "pod/hook created", HookType: hookType, HookPhase: OperationSucceeded, SyncPhase: SyncPhase(hookType)}))
}

func TestPostDeleteHook(t *testing.T) {
	Given(t).
		Path("post-delete-hook").
		When().
		CreateApp().
		Refresh(RefreshTypeNormal).
		Delete(true).
		Then().
		Expect(DoesNotExist()).
		AndAction(func() {
			hooks, err := KubeClientset.CoreV1().Pods(DeploymentNamespace()).List(t.Context(), metav1.ListOptions{})
			require.NoError(t, err)
			assert.Len(t, hooks.Items, 1)
			assert.Equal(t, "hook", hooks.Items[0].Name)
		})
}

// make sure that hooks do not appear in "argocd app diff"
func TestHookDiff(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		CreateApp().
		Then().
		And(func(_ *Application) {
			output, err := RunCli("app", "diff", Name())
			require.Error(t, err)
			assert.Contains(t, output, "name: pod")
			assert.NotContains(t, output, "name: hook")
		})
}

// make sure that if pre-sync fails, we fail the app and we do not create the pod
func TestPreSyncHookFailure(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "PreSync"}}]`).
		// make hook fail
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command", "value": ["false"]}]`).
		CreateApp().
		IgnoreErrors().
		Sync().
		Then().
		Expect(Error("hook    Failed   Synced     PreSync  container \"main\" failed", "")).
		// make sure resource are also printed
		Expect(Error("pod  OutOfSync  Missing", "")).
		Expect(OperationPhaseIs(OperationFailed)).
		// if a pre-sync hook fails, we should not start the main sync
		Expect(SyncStatusIs(SyncStatusCodeOutOfSync)).
		Expect(ResourceResultNumbering(1)).
		Expect(ResourceSyncStatusIs("Pod", "pod", SyncStatusCodeOutOfSync))
}

// make sure that if sync fails, we fail the app and we did create the pod
func TestSyncHookFailure(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		// make hook fail
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		IgnoreErrors().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationFailed)).
		// even thought the hook failed, we expect the pod to be in sync
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(ResourceResultNumbering(2)).
		Expect(ResourceSyncStatusIs("Pod", "pod", SyncStatusCodeSynced))
}

// make sure that if the deployments fails, we still get success and synced
func TestSyncHookResourceFailure(t *testing.T) {
	Given(t).
		Path("hook-and-deployment").
		When().
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(HealthIs(health.HealthStatusProgressing))
}

// make sure that if post-sync fails, we fail the app and we did not create the pod
func TestPostSyncHookFailure(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "PostSync"}}]`).
		// make hook fail
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		IgnoreErrors().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationFailed)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(ResourceResultNumbering(2)).
		Expect(ResourceSyncStatusIs("Pod", "pod", SyncStatusCodeSynced))
}

// make sure that if the pod fails, we do not run the post-sync hook
func TestPostSyncHookPodFailure(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		IgnoreErrors().
		PatchFile("hook.yaml", `[{"op": "add", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "PostSync"}}]`).
		// make pod fail
		PatchFile("pod.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		Sync().
		Then().
		// TODO - I feel like this should be a failure, not success
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(ResourceSyncStatusIs("Pod", "pod", SyncStatusCodeSynced)).
		Expect(ResourceHealthIs("Pod", "pod", health.HealthStatusDegraded)).
		Expect(ResourceResultNumbering(1)).
		Expect(NotPod(func(p corev1.Pod) bool { return p.Name == "hook" }))
}

func TestSyncFailHookPodFailure(t *testing.T) {
	// Tests that a SyncFail hook will successfully run upon a pod failure (which leads to a sync failure)
	Given(t).
		Path("hook").
		When().
		IgnoreErrors().
		AddFile("sync-fail-hook.yaml", `
apiVersion: v1
kind: Pod
metadata:
  annotations:
    argocd.argoproj.io/hook: SyncFail
  name: sync-fail-hook
spec:
  containers:
    - command:
        - "true"
      image: "quay.io/argoprojlabs/argocd-e2e-container:0.1"
      imagePullPolicy: IfNotPresent
      name: main
  restartPolicy: Never
`).
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "PostSync"}}]`).
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(ResourceResultIs(ResourceResult{Version: "v1", Kind: "Pod", Namespace: DeploymentNamespace(), Images: []string{"quay.io/argoprojlabs/argocd-e2e-container:0.1"}, Name: "sync-fail-hook", Message: "pod/sync-fail-hook created", HookType: HookTypeSyncFail, HookPhase: OperationSucceeded, SyncPhase: SyncPhaseSyncFail})).
		Expect(OperationPhaseIs(OperationFailed))
}

func TestSyncFailHookPodFailureSyncFailFailure(t *testing.T) {
	// Tests that a failing SyncFail hook will successfully be marked as failed
	Given(t).
		Path("hook").
		When().
		IgnoreErrors().
		AddFile("successful-sync-fail-hook.yaml", `
apiVersion: v1
kind: Pod
metadata:
  annotations:
    argocd.argoproj.io/hook: SyncFail
  name: successful-sync-fail-hook
spec:
  containers:
    - command:
        - "true"
      image: "quay.io/argoprojlabs/argocd-e2e-container:0.1"
      imagePullPolicy: IfNotPresent
      name: main
  restartPolicy: Never
`).
		AddFile("failed-sync-fail-hook.yaml", `
apiVersion: v1
kind: Pod
metadata:
  annotations:
    argocd.argoproj.io/hook: SyncFail
  name: failed-sync-fail-hook
spec:
  containers:
    - command:
        - "false"
      image: "quay.io/argoprojlabs/argocd-e2e-container:0.1"
      imagePullPolicy: IfNotPresent
      name: main
  restartPolicy: Never
`).
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "PostSync"}}]`).
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(ResourceResultIs(ResourceResult{Version: "v1", Kind: "Pod", Namespace: DeploymentNamespace(), Name: "successful-sync-fail-hook", Images: []string{"quay.io/argoprojlabs/argocd-e2e-container:0.1"}, Message: "pod/successful-sync-fail-hook created", HookType: HookTypeSyncFail, HookPhase: OperationSucceeded, SyncPhase: SyncPhaseSyncFail})).
		Expect(ResourceResultIs(ResourceResult{Version: "v1", Kind: "Pod", Namespace: DeploymentNamespace(), Name: "failed-sync-fail-hook", Images: []string{"quay.io/argoprojlabs/argocd-e2e-container:0.1"}, Message: `container "main" failed with exit code 1`, HookType: HookTypeSyncFail, HookPhase: OperationFailed, SyncPhase: SyncPhaseSyncFail})).
		Expect(OperationPhaseIs(OperationFailed))
}

// make sure that we delete the hook on success
func TestHookDeletePolicyHookSucceededHookExit0(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1hook-delete-policy", "value": "HookSucceeded"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(NotPod(func(p corev1.Pod) bool { return p.Name == "hook" }))
}

// make sure that we delete the hook on failure, if policy is set
func TestHookDeletePolicyHookSucceededHookExit1(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1hook-delete-policy", "value": "HookSucceeded"}]`).
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		IgnoreErrors().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationFailed)).
		Expect(ResourceResultNumbering(2)).
		Expect(Pod(func(p corev1.Pod) bool { return p.Name == "hook" }))
}

// make sure that we do NOT delete the hook on success if failure policy is set
func TestHookDeletePolicyHookFailedHookExit0(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1hook-delete-policy", "value": "HookFailed"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(ResourceResultNumbering(2)).
		Expect(Pod(func(p corev1.Pod) bool { return p.Name == "hook" }))
}

// make sure that we do delete the hook on failure if failure policy is set
func TestHookDeletePolicyHookFailedHookExit1(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		IgnoreErrors().
		PatchFile("hook.yaml", `[{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1hook-delete-policy", "value": "HookFailed"}]`).
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/spec/containers/0/command/0", "value": "false"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationFailed)).
		Expect(ResourceResultNumbering(2)).
		Expect(NotPod(func(p corev1.Pod) bool { return p.Name == "hook" }))
}

// make sure that we can run the hook twice
func TestHookBeforeHookCreation(t *testing.T) {
	var creationTimestamp1 string
	Given(t).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1hook-delete-policy", "value": "BeforeHookCreation"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(HealthIs(health.HealthStatusHealthy)).
		Expect(ResourceResultNumbering(2)).
		// the app will be in health+n-sync before this hook has run
		Expect(Pod(func(p corev1.Pod) bool { return p.Name == "hook" })).
		And(func(_ *Application) {
			var err error
			creationTimestamp1, err = getCreationTimestamp()
			require.NoError(t, err)
			assert.NotEmpty(t, creationTimestamp1)
			// pause to ensure that timestamp will change
			time.Sleep(1 * time.Second)
		}).
		When().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(HealthIs(health.HealthStatusHealthy)).
		Expect(ResourceResultNumbering(2)).
		Expect(Pod(func(p corev1.Pod) bool { return p.Name == "hook" })).
		And(func(_ *Application) {
			creationTimestamp2, err := getCreationTimestamp()
			require.NoError(t, err)
			assert.NotEmpty(t, creationTimestamp2)
			assert.NotEqual(t, creationTimestamp1, creationTimestamp2)
		})
}

// edge-case where we are unable to delete the hook because it is still running
func TestHookBeforeHookCreationFailure(t *testing.T) {
	Given(t).
		Timeout(1).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[
	{"op": "add", "path": "/metadata/annotations/argocd.argoproj.io~1hook-delete-policy", "value": "BeforeHookCreation"},
	{"op": "replace", "path": "/spec/containers/0/command", "value": ["sleep", "3"]}
]`).
		CreateApp().
		IgnoreErrors().
		Sync().
		DoNotIgnoreErrors().
		TerminateOp().
		Then().
		Expect(OperationPhaseIs(OperationFailed)).
		Expect(ResourceResultNumbering(2))
}

func getCreationTimestamp() (string, error) {
	return Run(".", "kubectl", "-n", DeploymentNamespace(), "get", "pod", "hook", "-o", "jsonpath={.metadata.creationTimestamp}")
}

// make sure that we never create something annotated with Skip
func TestHookSkip(t *testing.T) {
	Given(t).
		Path("hook").
		When().
		// should not create this pod
		PatchFile("pod.yaml", `[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "Skip"}}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(ResourceResultNumbering(1)).
		Expect(NotPod(func(p corev1.Pod) bool { return p.Name == "pod" }))
}

// make sure that we do NOT name non-hook resources in they are unnamed
func TestNamingNonHookResource(t *testing.T) {
	Given(t).
		Async(true).
		Path("hook").
		When().
		PatchFile("pod.yaml", `[{"op": "remove", "path": "/metadata/name"}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationFailed))
}

// make sure that we name hook resources in they are unnamed
func TestAutomaticallyNamingUnnamedHook(t *testing.T) {
	Given(t).
		Async(true).
		Path("hook").
		When().
		PatchFile("hook.yaml", `[{"op": "remove", "path": "/metadata/name"}]`).
		// make this part of two sync tasks
		PatchFile("hook.yaml", `[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": "PreSync,PostSync"}}]`).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		And(func(app *Application) {
			resources := app.Status.OperationState.SyncResult.Resources
			assert.Len(t, resources, 3)
			// make sure we don't use the same name
			assert.Contains(t, resources[0].Name, "presync")
			assert.Contains(t, resources[2].Name, "postsync")
		})
}

func TestHookFinalizerPreSync(t *testing.T) {
	testHookFinalizer(t, HookTypePreSync)
}

func TestHookFinalizerSync(t *testing.T) {
	testHookFinalizer(t, HookTypeSync)
}

func TestHookFinalizerPostSync(t *testing.T) {
	testHookFinalizer(t, HookTypePostSync)
}

func testHookFinalizer(t *testing.T, hookType HookType) {
	t.Helper()
	Given(t).
		And(func() {
			require.NoError(t, SetResourceOverrides(map[string]ResourceOverride{
				lua.GetConfigMapKey(schema.FromAPIVersionAndKind("batch/v1", "Job")): {
					HealthLua: `
						local hs = {}
						hs.status = "Healthy"
						if obj.metadata.deletionTimestamp == nil then
							hs.status = "Progressing"
							hs.message = "Waiting to be externally deleted"
							return hs
						end
						if obj.metadata.finalizers ~= nil  then
							for i, finalizer in ipairs(obj.metadata.finalizers) do
								if finalizer == "argocd.argoproj.io/hook-finalizer" then
									hs.message = "Resource has finalizer"
									return hs
								end
							end
						end
						hs.message = "no finalizer for a hook is wrong"
						return hs`,
				},
			}))
		}).
		Path("hook-resource-deleted-externally").
		When().
		PatchFile("hook.yaml", fmt.Sprintf(`[{"op": "replace", "path": "/metadata/annotations", "value": {"argocd.argoproj.io/hook": %q}}]`, hookType)).
		CreateApp().
		Sync().
		Then().
		Expect(OperationPhaseIs(OperationSucceeded)).
		Expect(SyncStatusIs(SyncStatusCodeSynced)).
		Expect(ResourceSyncStatusIs("Pod", "pod", SyncStatusCodeSynced)).
		Expect(ResourceHealthIs("Pod", "pod", health.HealthStatusHealthy)).
		Expect(ResourceResultNumbering(2)).
		Expect(ResourceResultIs(ResourceResult{Group: "batch", Version: "v1", Kind: "Job", Namespace: DeploymentNamespace(), Name: "hook", Images: []string{"quay.io/argoprojlabs/argocd-e2e-container:0.1"}, Message: "Resource has finalizer", HookType: hookType, HookPhase: OperationSucceeded, SyncPhase: SyncPhase(hookType)}))
}
