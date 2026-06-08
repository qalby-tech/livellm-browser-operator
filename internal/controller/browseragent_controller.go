package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	browserv1 "github.com/livellm/browser-operator/api/v1alpha1"
)

const agentFinalizerName = "livellm.io/browseragent-cleanup"

type BrowserAgentReconciler struct {
	client.Client
	Scheme                 *runtime.Scheme
	DefaultAgentImage      string
	DefaultAgentPullPolicy string
	DefaultAgentEnv        []corev1.EnvVar
	DefaultAgentResources  *browserv1.ResourcesSpec
}

func (r *BrowserAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&browserv1.BrowserAgent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		// re-resolve when a target Browser/Controller becomes ready or its ws changes
		Watches(&browserv1.Browser{}, handler.EnqueueRequestsFromMapFunc(r.mapToAgents)).
		Watches(&browserv1.Controller{}, handler.EnqueueRequestsFromMapFunc(r.mapToAgents)).
		Complete(r)
}

func (r *BrowserAgentReconciler) mapToAgents(ctx context.Context, obj client.Object) []ctrl.Request {
	var agents browserv1.BrowserAgentList
	if err := r.List(ctx, &agents, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	reqs := make([]ctrl.Request, 0, len(agents.Items))
	for _, a := range agents.Items {
		reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Name: a.Name, Namespace: a.Namespace}})
	}
	return reqs
}

func (r *BrowserAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var agent browserv1.BrowserAgent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !agent.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&agent, agentFinalizerName) {
			controllerutil.RemoveFinalizer(&agent, agentFinalizerName)
			if err := r.Update(ctx, &agent); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&agent, agentFinalizerName) {
		controllerutil.AddFinalizer(&agent, agentFinalizerName)
		if err := r.Update(ctx, &agent); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Resolve the CDP target from spec.target by reading Browser/Controller status.
	tgt, phase, msg := r.resolveTarget(ctx, &agent)
	if phase == browserv1.BrowserAgentPhaseFailed {
		return r.setAgentStatus(ctx, &agent, phase, msg, resolvedTarget{}, controllerRequeuePending)
	}
	if phase == browserv1.BrowserAgentPhasePending {
		// still deploy the Deployment/Service (scaled by running) so the pod is
		// ready to receive a target once it resolves; report Pending meanwhile.
		if err := r.ensureAgentResources(ctx, &agent, tgt); err != nil {
			logger.Error(err, "ensure agent resources (pending target)")
		}
		return r.setAgentStatus(ctx, &agent, phase, msg, tgt, controllerRequeuePending)
	}

	if err := r.ensureAgentResources(ctx, &agent, tgt); err != nil {
		logger.Error(err, "failed to ensure agent resources")
		return ctrl.Result{RequeueAfter: controllerRequeueRetry}, nil
	}

	return r.reconcileAgentStatus(ctx, &agent, tgt)
}

// resolveTarget computes the CDP wiring from spec.target. Returns a phase of
// Pending while a referenced Browser/Controller isn't ready, Failed for an
// invalid/empty target, and "" (running-ready) when fully resolved.
func (r *BrowserAgentReconciler) resolveTarget(ctx context.Context, a *browserv1.BrowserAgent) (resolvedTarget, browserv1.BrowserAgentPhase, string) {
	t := a.Spec.Target

	switch {
	case t.ExternalWsURL != "":
		return resolvedTarget{cdpWsURL: t.ExternalWsURL}, "", ""

	case t.BrowserRef != "":
		var br browserv1.Browser
		if err := r.Get(ctx, types.NamespacedName{Name: t.BrowserRef, Namespace: a.Namespace}, &br); err != nil {
			if apierrors.IsNotFound(err) {
				return resolvedTarget{}, browserv1.BrowserAgentPhasePending, fmt.Sprintf("Browser %q not found", t.BrowserRef)
			}
			return resolvedTarget{}, browserv1.BrowserAgentPhasePending, "error reading Browser"
		}
		if br.Status.WsURL == "" {
			return resolvedTarget{}, browserv1.BrowserAgentPhasePending, fmt.Sprintf("waiting for Browser %q CDP endpoint", t.BrowserRef)
		}
		return resolvedTarget{cdpWsURL: br.Status.WsURL}, "", ""

	case t.ControllerRef != "":
		var c browserv1.Controller
		if err := r.Get(ctx, types.NamespacedName{Name: t.ControllerRef, Namespace: a.Namespace}, &c); err != nil {
			if apierrors.IsNotFound(err) {
				return resolvedTarget{}, browserv1.BrowserAgentPhasePending, fmt.Sprintf("Controller %q not found", t.ControllerRef)
			}
			return resolvedTarget{}, browserv1.BrowserAgentPhasePending, "error reading Controller"
		}
		if c.Status.URL == "" {
			return resolvedTarget{}, browserv1.BrowserAgentPhasePending, fmt.Sprintf("waiting for Controller %q", t.ControllerRef)
		}
		// pick the registered browser: the named BrowserID, or the only one.
		ws, id := "", t.BrowserID
		regs := c.Status.RegisteredBrowsers
		if id != "" {
			for _, rb := range regs {
				if rb.ProfileUID == id {
					ws = rb.WsURL
					break
				}
			}
		} else if len(regs) == 1 {
			ws, id = regs[0].WsURL, regs[0].ProfileUID
		}
		if ws == "" {
			return resolvedTarget{controllerURL: c.Status.URL, browserID: id},
				browserv1.BrowserAgentPhasePending,
				"waiting for target browser to register with the controller"
		}
		return resolvedTarget{cdpWsURL: ws, controllerURL: c.Status.URL, browserID: id}, "", ""
	}

	return resolvedTarget{}, browserv1.BrowserAgentPhaseFailed, "spec.target must set one of browserRef / controllerRef / externalWsUrl"
}

func (r *BrowserAgentReconciler) ensureAgentResources(ctx context.Context, a *browserv1.BrowserAgent, tgt resolvedTarget) error {
	deploy := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, deploy, func() error {
		applyAgentDeploymentSpec(deploy, a, tgt, r.DefaultAgentImage, r.DefaultAgentPullPolicy, r.DefaultAgentEnv, r.DefaultAgentResources)
		return controllerutil.SetControllerReference(a, deploy, r.Scheme)
	}); err != nil {
		return err
	}

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		applyAgentServiceSpec(svc, a)
		return controllerutil.SetControllerReference(a, svc, r.Scheme)
	})
	return err
}

func (r *BrowserAgentReconciler) reconcileAgentStatus(ctx context.Context, a *browserv1.BrowserAgent, tgt resolvedTarget) (ctrl.Result, error) {
	if !agentWorkloadWanted(a) {
		return r.setAgentStatus(ctx, a, browserv1.BrowserAgentPhaseStopped, "Agent stopped (running=false)", tgt, controllerRequeueReady)
	}

	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(a.Namespace),
		client.MatchingLabels(agentSelectorLabels(a.Name)),
	); err != nil {
		return ctrl.Result{}, err
	}

	var readyPod *corev1.Pod
	for i := range podList.Items {
		p := &podList.Items[i]
		if p.Status.Phase == corev1.PodRunning && isPodReady(p) {
			readyPod = p
			break
		}
	}
	if readyPod == nil {
		return r.setAgentStatus(ctx, a, browserv1.BrowserAgentPhaseCreating, "Waiting for agent pod to be ready", tgt, controllerRequeuePending)
	}

	a.Status.PodName = readyPod.Name
	return r.setAgentStatus(ctx, a, browserv1.BrowserAgentPhaseRunning, "Agent is ready", tgt, controllerRequeueReady)
}

func (r *BrowserAgentReconciler) setAgentStatus(
	ctx context.Context, a *browserv1.BrowserAgent, phase browserv1.BrowserAgentPhase,
	message string, tgt resolvedTarget, requeue time.Duration,
) (ctrl.Result, error) {
	url := ""
	if phase == browserv1.BrowserAgentPhaseRunning {
		url = fmt.Sprintf("http://%s.%s:%d", a.Name, a.Namespace, agentPort)
	}
	a.Status.Phase = phase
	a.Status.Message = message
	a.Status.URL = url
	a.Status.ResolvedWsURL = tgt.cdpWsURL
	if a.Spec.Control != nil {
		a.Status.ControlURL = a.Spec.Control.URL
	}
	if err := r.Status().Update(ctx, a); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}
