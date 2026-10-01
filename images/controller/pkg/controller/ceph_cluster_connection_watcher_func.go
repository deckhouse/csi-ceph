/*
Copyright 2025 Flant JSC

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
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v8/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/deckhouse/csi-ceph/api/v1alpha1"
	"github.com/deckhouse/csi-ceph/images/controller/pkg/internal"
	"github.com/deckhouse/sds-common-lib/conditions"
	"github.com/deckhouse/sds-common-lib/logger"
)

func validateCephClusterConnectionSpec(cephClusterConnection *v1alpha1.CephClusterConnection) (bool, string) {
	if cephClusterConnection.DeletionTimestamp != nil {
		return true, ""
	}

	var (
		failedMsgBuilder strings.Builder
		validationPassed = true
	)

	failedMsgBuilder.WriteString("Validation of CephClusterConnection failed: ")

	if cephClusterConnection.Spec.ClusterID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.clusterID field is empty; ")
	}

	if len(cephClusterConnection.Spec.Monitors) == 0 {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.monitors field is empty; ")
	}

	if cephClusterConnection.Spec.UserID == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.userID field is empty; ")
	}

	if cephClusterConnection.Spec.UserKey == "" {
		validationPassed = false
		failedMsgBuilder.WriteString("the spec.userKey field is empty; ")
	}

	return validationPassed, failedMsgBuilder.String()
}

// updateCephClusterConnectionStatus records the outcome of a reconcile pass.
//
// The Ready condition is the source of truth; phase and reason are derived from
// it and kept for the printer column and for tooling that predates conditions.
//
// reconcileErr is the error the pass returned, or nil on success. msg is the
// human-readable summary the pass produced, which may be empty. extra are the
// other conditions the pass evaluated, written in the same update.
func updateCephClusterConnectionStatus(
	ctx context.Context,
	cl client.Client,
	cephClusterConnection *v1alpha1.CephClusterConnection,
	reconcileErr error,
	msg string,
	extra ...metav1.Condition,
) error {
	// The generation that was actually reconciled. Taken from the object the
	// caller reconciled rather than from the one read inside UpdateStatus: if
	// the spec changed in between, observedGeneration must still point at the
	// generation this verdict is about.
	generation := cephClusterConnection.Generation

	cond := conditions.Ready(generation, reconcileErr)
	if cond.Message == "" {
		cond.Message = msg
	}
	var inUse *secretInUseError
	if errors.As(reconcileErr, &inUse) {
		cond.Reason = inUse.reason()
	}

	return conditions.UpdateStatus(ctx, cl, cephClusterConnection, func(cc *v1alpha1.CephClusterConnection) {
		if cc.Status == nil {
			cc.Status = &v1alpha1.CephClusterConnectionStatus{}
		}
		cc.Status.ObservedGeneration = generation
		conditions.Set(&cc.Status.Conditions, cond)
		for _, c := range extra {
			conditions.Set(&cc.Status.Conditions, c)
		}
		cc.Status.Phase = phaseFromReady(cc.Status.Conditions)
		cc.Status.Reason = msg
	})
}

// clusterConfigConflictCondition builds the ClusterConfigConflict condition
// from the message generateSharedClusterConfig gave for a connection, empty
// when the config.json entry serves all its volumes.
func clusterConfigConflictCondition(generation int64, message string) metav1.Condition {
	if message == "" {
		return metav1.Condition{
			Type:               v1alpha1.ConditionTypeClusterConfigConflict,
			Status:             metav1.ConditionFalse,
			Reason:             v1alpha1.ReasonServedByClusterConfig,
			Message:            "the config.json entry of the cluster serves every volume of this connection",
			ObservedGeneration: generation,
		}
	}
	return metav1.Condition{
		Type:               v1alpha1.ConditionTypeClusterConfigConflict,
		Status:             metav1.ConditionTrue,
		Reason:             v1alpha1.ReasonVolumesNeedOtherSettings,
		Message:            conditions.TruncateMessage(message),
		ObservedGeneration: generation,
	}
}

// publishClusterConfigConflict writes the ClusterConfigConflict condition of
// the CephClusterConnection name, which shares the config.json entry of the
// connection being reconciled: whose settings the entry holds is decided for
// all of them at once.
func publishClusterConfigConflict(ctx context.Context, cl client.Client, name, message string) error {
	connection := &v1alpha1.CephClusterConnection{ObjectMeta: metav1.ObjectMeta{Name: name}}
	return conditions.UpdateStatus(ctx, cl, connection, func(cc *v1alpha1.CephClusterConnection) {
		if cc.Status == nil {
			cc.Status = &v1alpha1.CephClusterConnectionStatus{}
		}
		conditions.Set(&cc.Status.Conditions, clusterConfigConflictCondition(cc.Generation, message))
	})
}

func reconcileConfigMap(ctx context.Context, cl client.Client, log logger.Logger, configMapList *corev1.ConfigMapList, cephClusterConnection *v1alpha1.CephClusterConnection, desiredClusterConfig *v1alpha1.ClusterConfig, configMapNamespace, configMapName string) (shouldRequeue bool, msg string, err error) {
	var configMap *corev1.ConfigMap
	for _, cm := range configMapList.Items {
		if cm.Name == configMapName {
			configMap = &cm
			break
		}
	}

	if configMap == nil {
		if desiredClusterConfig != nil {
			return createConfigMap(ctx, cl, log, *desiredClusterConfig, configMapNamespace, configMapName)
		}
		log.Info("CephClusterConnection is being deleted and Config map not found.")
		return false, "", nil
	}

	err = updateConfigMapIfNeeded(ctx, cl, log, configMap, cephClusterConnection.Spec.ClusterID, desiredClusterConfig)
	if err != nil {
		if errors.Is(err, errUpdateIncompleted) {
			return true, "", nil
		}
		return true, err.Error(), err
	}

	return false, fmt.Sprintf("Successfully reconciled ConfigMap %s", configMapName), nil
}

func getClusterConfigsFromConfigMap(configMap *corev1.ConfigMap) ([]v1alpha1.ClusterConfig, error) {
	var clusterConfigs []v1alpha1.ClusterConfig

	jsonData, ok := configMap.Data["config.json"]
	if !ok {
		return nil, fmt.Errorf("[getClusterConfigsFromConfigMap] config.json key not found in the ConfigMap %s", configMap.Name)
	}

	err := json.Unmarshal([]byte(jsonData), &clusterConfigs)
	if err != nil {
		return nil, fmt.Errorf("[getClusterConfigsFromConfigMap] unable to unmarshal data from the ConfigMap %s: %w", configMap.Name, err)
	}

	return clusterConfigs, nil
}

func generateClusterConfig(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) v1alpha1.ClusterConfig {
	// Generate secret name and namespace
	secretName := internal.CephClusterConnectionSecretPrefix + cephClusterConnection.Name

	secretRef := v1alpha1.SecretReference{
		Name:      secretName,
		Namespace: controllerNamespace,
	}

	cephFs := v1alpha1.CephFSConfig{
		ControllerPublishSecretRef: secretRef,
	}

	// if SubvolumeGroup given in connection config we should put it to configmap
	if cephClusterConnection.Spec.CephFS != nil && cephClusterConnection.Spec.CephFS.SubvolumeGroup != "" {
		cephFs.SubvolumeGroup = cephClusterConnection.Spec.CephFS.SubvolumeGroup
	}

	rbd := v1alpha1.RBDConfig{
		ControllerPublishSecretRef: secretRef,
	}

	clusterConfig := v1alpha1.ClusterConfig{
		ClusterID: cephClusterConnection.Spec.ClusterID,
		Monitors:  cephClusterConnection.Spec.Monitors,
		CephFS:    cephFs,
		RBD:       rbd,
	}

	return clusterConfig
}

// getDesiredClusterConfig returns the config.json entry for the clusterID of
// cephClusterConnection, or nil when no connection is left for that clusterID
// and the entry has to go, along with the conflicts of generateSharedClusterConfig.
// current is the entry config.json holds now, nil when there is none.
func getDesiredClusterConfig(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, current *v1alpha1.ClusterConfig, usage volumeUsage, controllerNamespace string) (*v1alpha1.ClusterConfig, map[string]string, error) {
	connectionList := &v1alpha1.CephClusterConnectionList{}
	if err := cl.List(ctx, connectionList); err != nil {
		return nil, nil, fmt.Errorf("[getDesiredClusterConfig] unable to list CephClusterConnections: %w", err)
	}

	storageClassList := &v1alpha1.CephStorageClassList{}
	if err := cl.List(ctx, storageClassList); err != nil {
		return nil, nil, fmt.Errorf("[getDesiredClusterConfig] unable to list CephStorageClasses: %w", err)
	}

	clusterConfig, conflicts := generateSharedClusterConfig(log, cephClusterConnection, connectionList.Items, storageClassList.Items, current, usage, controllerNamespace)
	return clusterConfig, conflicts, nil
}

// generateSharedClusterConfig builds the config.json entry that every
// CephClusterConnection with the clusterID of cephClusterConnection shares:
// ceph-csi keys that file by clusterID alone. Each driver gets the secret and
// settings of one connection, chosen by pickClusterConnection, so that the
// entry no longer depends on which connection was reconciled last. The
// monitors are those of all these connections, as the entry serves the
// volumes of each of them.
//
// It also returns, for every connection the entry is built from, the message
// of its ClusterConfigConflict condition: empty when the entry serves all its
// volumes.
func generateSharedClusterConfig(log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, connections []v1alpha1.CephClusterConnection, storageClasses []v1alpha1.CephStorageClass, current *v1alpha1.ClusterConfig, usage volumeUsage, controllerNamespace string) (*v1alpha1.ClusterConfig, map[string]string) {
	clusterID := cephClusterConnection.Spec.ClusterID

	// The cache may not have caught up with the object this pass was started
	// for, so that object replaces its listed copy, even when it is itself
	// left out.
	seen := make(map[string]bool, len(connections)+1)
	candidates := make([]v1alpha1.CephClusterConnection, 0, len(connections)+1)
	for _, connection := range append([]v1alpha1.CephClusterConnection{*cephClusterConnection}, connections...) {
		if seen[connection.Name] {
			continue
		}
		seen[connection.Name] = true

		if connection.Spec.ClusterID != clusterID {
			continue
		}
		// A connection being deleted stays until no volume needs its secret,
		// and ceph-csi needs the monitors of the entry for these volumes.
		if connection.DeletionTimestamp != nil && !usage.secretNeeded(controllerNamespace, internal.CephClusterConnectionSecretPrefix+connection.Name) {
			continue
		}
		if valid, _ := validateCephClusterConnectionSpec(&connection); !valid {
			continue
		}
		candidates = append(candidates, connection)
	}

	if len(candidates) == 0 {
		return nil, nil
	}

	slices.SortFunc(candidates, func(a, b v1alpha1.CephClusterConnection) int { return strings.Compare(a.Name, b.Name) })

	rbdConnection := pickClusterConnection(clusterID, candidates, storageClasses, current, usage, v1alpha1.CephStorageClassTypeRBD, controllerNamespace)
	cephFSConnection := pickClusterConnection(clusterID, candidates, storageClasses, current, usage, v1alpha1.CephStorageClassTypeCephFS, controllerNamespace)

	clusterConfig := generateClusterConfig(rbdConnection, controllerNamespace)
	clusterConfig.CephFS = generateClusterConfig(cephFSConnection, controllerNamespace).CephFS
	clusterConfig.Monitors = sharedMonitors(rbdConnection, candidates)

	rbdConflicts := configConflicts(usage, clusterID, v1alpha1.CephStorageClassTypeRBD, controllerNamespace, rbdConnection, candidates)
	cephFSConflicts := configConflicts(usage, clusterID, v1alpha1.CephStorageClassTypeCephFS, controllerNamespace, cephFSConnection, candidates)

	conflicts := make(map[string]string, len(candidates))
	for i := range candidates {
		name := candidates[i].Name

		var parts []string
		if pvNames := rbdConflicts[name]; len(pvNames) > 0 {
			parts = append(parts, fmt.Sprintf("%d RBD PersistentVolumes %v need another user than the one of the CephClusterConnection %s, which the entry holds, and fail to detach", len(pvNames), listedNames(pvNames), rbdConnection.Name))
		}
		if pvNames := cephFSConflicts[name]; len(pvNames) > 0 {
			parts = append(parts, fmt.Sprintf("%d CephFS PersistentVolumes %v need another subvolume group than %s of the CephClusterConnection %s, which the entry holds, and can no longer be deleted, expanded or snapshotted", len(pvNames), listedNames(pvNames), subvolumeGroupOf(cephFSConnection), cephFSConnection.Name))
		}

		conflicts[name] = ""
		if len(parts) > 0 {
			conflicts[name] = fmt.Sprintf("config.json has one entry for the cluster %s: %s", clusterID, strings.Join(parts, "; "))
			log.Debug(fmt.Sprintf("the CephClusterConnection %s: %s", name, conflicts[name]))
		}
	}

	return &clusterConfig, conflicts
}

// sharedMonitors returns the monitors of first, followed by those of the
// other candidates that first does not list, so that one connection with an
// outdated list does not cut the volumes of the others off the cluster.
func sharedMonitors(first *v1alpha1.CephClusterConnection, candidates []v1alpha1.CephClusterConnection) []string {
	monitors := slices.Clone(first.Spec.Monitors)
	for i := range candidates {
		for _, monitor := range candidates[i].Spec.Monitors {
			if !slices.Contains(monitors, monitor) {
				monitors = append(monitors, monitor)
			}
		}
	}
	return monitors
}

// pickClusterConnection returns the connection whose settings the
// storageClassType driver takes from config.json, in this order:
//   - the one the current entry holds, if existing volumes depend on it: the
//     volumes that work with the entry keep working, and an upgrade from the
//     time the entry followed the connection reconciled last moves nothing;
//   - the first of the name-sorted candidates that existing volumes depend on
//     (see volumesUsingConfig);
//   - the first one a CephStorageClass of storageClassType refers to, a
//     CephStorageClass being deleted included;
//   - the first one.
func pickClusterConnection(clusterID string, candidates []v1alpha1.CephClusterConnection, storageClasses []v1alpha1.CephStorageClass, current *v1alpha1.ClusterConfig, usage volumeUsage, storageClassType, controllerNamespace string) *v1alpha1.CephClusterConnection {
	dependents := volumesUsingConfig(usage, clusterID, storageClassType, controllerNamespace)
	if held := heldConnectionName(current, storageClassType, controllerNamespace); held != "" {
		for i := range candidates {
			if candidates[i].Name == held && dependents.dependOn(&candidates[i]) {
				return &candidates[i]
			}
		}
	}
	for i := range candidates {
		if dependents.dependOn(&candidates[i]) {
			return &candidates[i]
		}
	}

	for i := range candidates {
		if slices.ContainsFunc(storageClasses, func(storageClass v1alpha1.CephStorageClass) bool {
			return storageClass.Spec.Type == storageClassType && storageClass.Spec.ClusterConnectionName == candidates[i].Name
		}) {
			return &candidates[i]
		}
	}

	return &candidates[0]
}

// heldConnectionName returns the connection whose secret the
// storageClassType part of entry names, or an empty name.
func heldConnectionName(entry *v1alpha1.ClusterConfig, storageClassType, controllerNamespace string) string {
	if entry == nil {
		return ""
	}
	ref := entry.RBD.ControllerPublishSecretRef
	if storageClassType == v1alpha1.CephStorageClassTypeCephFS {
		ref = entry.CephFS.ControllerPublishSecretRef
	}
	if ref.Namespace != controllerNamespace {
		return ""
	}
	name, _ := strings.CutPrefix(ref.Name, internal.CephClusterConnectionSecretPrefix)
	if name == ref.Name {
		return ""
	}
	return name
}

// currentClusterConfig returns the entry for clusterID that the ConfigMap
// configMapName of configMapList holds, or nil.
func currentClusterConfig(configMapList *corev1.ConfigMapList, configMapName, clusterID string) *v1alpha1.ClusterConfig {
	for i := range configMapList.Items {
		if configMapList.Items[i].Name != configMapName {
			continue
		}
		clusterConfigs, err := getClusterConfigsFromConfigMap(&configMapList.Items[i])
		if err != nil {
			return nil
		}
		if idx, ok := findClusterConfigByClusterID(clusterConfigs, clusterID); ok {
			return &clusterConfigs[idx]
		}
	}
	return nil
}

// configConflicts returns, by the name of the candidate they were created
// through, the sorted names of the storageClassType volumes that read
// config.json and need another user (RBD) or subvolume group (CephFS) than
// the one of chosen, which the entry holds.
func configConflicts(usage volumeUsage, clusterID, storageClassType, controllerNamespace string, chosen *v1alpha1.CephClusterConnection, candidates []v1alpha1.CephClusterConnection) map[string][]string {
	held := configSettingOf(chosen, storageClassType)

	conflicts := map[string][]string{}
	forEachVolumeUsingConfig(usage, clusterID, storageClassType, controllerNamespace, func(pv *corev1.PersistentVolume, subvolumeGroup, connectionName string) {
		i := slices.IndexFunc(candidates, func(c v1alpha1.CephClusterConnection) bool { return c.Name == connectionName })
		if i < 0 {
			return
		}
		needed := subvolumeGroup
		if needed == "" {
			needed = configSettingOf(&candidates[i], storageClassType)
		}
		if needed != held {
			conflicts[connectionName] = append(conflicts[connectionName], pv.Name)
		}
	})

	for _, pvNames := range conflicts {
		slices.Sort(pvNames)
	}
	return conflicts
}

// configSettingOf returns what the storageClassType volumes of connection take
// from config.json: the user for RBD, the subvolume group for CephFS.
func configSettingOf(connection *v1alpha1.CephClusterConnection, storageClassType string) string {
	if storageClassType == v1alpha1.CephStorageClassTypeCephFS {
		return subvolumeGroupOf(connection)
	}
	return connection.Spec.UserID
}

// subvolumeGroupOf returns the subvolume group ceph-csi uses with the entry of
// connection, which is csi when the entry names none.
func subvolumeGroupOf(connection *v1alpha1.CephClusterConnection) string {
	if connection.Spec.CephFS != nil && connection.Spec.CephFS.SubvolumeGroup != "" {
		return connection.Spec.CephFS.SubvolumeGroup
	}
	return cephCSIDefaultSubvolumeGroup
}

// configDependents is what the volumes that read config.json depend on there.
type configDependents struct {
	// connections are the connections whose secret the node-stage secret of
	// such a volume is.
	connections map[string]bool
	// subvolumeGroups are the groups the CephFS subvolumes lie in.
	subvolumeGroups map[string]bool
}

func (d configDependents) dependOn(connection *v1alpha1.CephClusterConnection) bool {
	return d.connections[connection.Name] || d.subvolumeGroups[subvolumeGroupOf(connection)]
}

// volumesUsingConfig returns what the storageClassType volumes of clusterID
// that read config.json depend on there: the subvolume group a CephFS volume
// lies in, and otherwise the connection a volume was created through (see
// forEachVolumeUsingConfig).
func volumesUsingConfig(usage volumeUsage, clusterID, storageClassType, controllerNamespace string) configDependents {
	dependents := configDependents{connections: map[string]bool{}, subvolumeGroups: map[string]bool{}}
	forEachVolumeUsingConfig(usage, clusterID, storageClassType, controllerNamespace, func(_ *corev1.PersistentVolume, subvolumeGroup, connectionName string) {
		switch {
		case subvolumeGroup != "":
			dependents.subvolumeGroups[subvolumeGroup] = true
		case connectionName != "":
			dependents.connections[connectionName] = true
		}
	})
	return dependents
}

// forEachVolumeUsingConfig calls visit for every storageClassType volume of
// clusterID that reads config.json and will still have its secrets read (see
// pvsNeedingSecret): a kept volume attached nowhere makes no CSI call that
// would read the entry, so it neither pins the entry nor conflicts with it.
// RBD volumes without a controller-publish
// secret of their own, provisioned before the StorageClass carried one, take
// the user from there. Every CephFS volume but a static one takes the
// subvolume group from there, and visit gets the group its subvolumePath lies
// in, which is the one the entry held when the volume was created and not
// necessarily the one of the connection it was created through; it gets an
// empty group when the path does not tell. visit also gets the connection the
// volume was created through, the one whose secret its node-stage secret is,
// or an empty name when there is none.
func forEachVolumeUsingConfig(usage volumeUsage, clusterID, storageClassType, controllerNamespace string, visit func(pv *corev1.PersistentVolume, subvolumeGroup, connectionName string)) {
	driver := GetStorageClassProvisioner(storageClassType)

	pvs := usage.pvs
	for i := range pvs {
		csi := pvs[i].Spec.CSI
		if csi == nil || csi.Driver != driver || csi.VolumeAttributes["clusterID"] != clusterID || !usage.needsSecrets(&pvs[i]) {
			continue
		}

		var subvolumeGroup string
		switch storageClassType {
		case v1alpha1.CephStorageClassTypeRBD:
			if csi.ControllerPublishSecretRef != nil {
				continue
			}
		case v1alpha1.CephStorageClassTypeCephFS:
			if static, _ := strconv.ParseBool(csi.VolumeAttributes["staticVolume"]); static {
				continue
			}
			subvolumeGroup, _ = subvolumeGroupOfPath(csi.VolumeAttributes["subvolumePath"])
		}

		var connectionName string
		if csi.NodeStageSecretRef != nil && csi.NodeStageSecretRef.Namespace == controllerNamespace {
			connectionName, _ = strings.CutPrefix(csi.NodeStageSecretRef.Name, internal.CephClusterConnectionSecretPrefix)
			if connectionName == csi.NodeStageSecretRef.Name {
				connectionName = ""
			}
		}

		visit(&pvs[i], subvolumeGroup, connectionName)
	}
}

// subvolumeGroupOfPath returns the group of a CephFS subvolume path, which
// ceph fs subvolume getpath gives as /volumes/<group>/<subvolume>/<uuid>.
func subvolumeGroupOfPath(path string) (string, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "volumes" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func createConfigMap(ctx context.Context, cl client.Client, log logger.Logger, clusterConfig v1alpha1.ClusterConfig, controllerNamespace, configMapName string) (shouldRequeue bool, msg string, err error) {
	log = log.Named("configmap").Named("create")

	log.Debug(fmt.Sprintf("starts creation of ConfigMap %s for the cluster %s", configMapName, clusterConfig.ClusterID))

	newConfigMap, err := generateNewConfigMap(clusterConfig, controllerNamespace, configMapName)
	if err != nil {
		err = fmt.Errorf("[createConfigMap] unable to generate the ConfigMap %s for the cluster %s: %w", configMapName, clusterConfig.ClusterID, err)
		return true, err.Error(), err
	}
	log.Debug(fmt.Sprintf("successfully generate the ConfigMap %s for the cluster %s", configMapName, clusterConfig.ClusterID))
	log.Trace(fmt.Sprintf("ConfigMap: %+v", newConfigMap))

	err = cl.Create(ctx, newConfigMap)
	if err != nil {
		err = fmt.Errorf("[createConfigMap] unable to create a ConfigMap %s for the cluster %s: %w", newConfigMap.Name, clusterConfig.ClusterID, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("successfully created ConfigMap %s for the cluster %s", newConfigMap.Name, clusterConfig.ClusterID))
	return false, fmt.Sprintf("Successfully created ConfigMap %s", newConfigMap.Name), nil
}

func generateNewConfigMap(clusterConfig v1alpha1.ClusterConfig, controllerNamespace, configMapName string) (*corev1.ConfigMap, error) {
	clusterConfigs := []v1alpha1.ClusterConfig{clusterConfig}
	jsonData, err := json.Marshal(clusterConfigs)
	if err != nil {
		return nil, fmt.Errorf("[generateConfigMap] unable to marshal clusterConfigs: %w", err)
	}

	if controllerNamespace == "" {
		return nil, fmt.Errorf("[generateConfigMap] controllerNamespace is empty")
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: controllerNamespace,
			Labels: map[string]string{
				internal.StorageManagedLabelKey: CephClusterConnectionCtrlName,
			},
			Finalizers: []string{CephClusterConnectionControllerFinalizerName},
		},
		Data: map[string]string{
			"config.json": string(jsonData),
		},
	}

	return configMap, nil
}

var errUpdateIncompleted = errors.New("update was not completed yet, should requeue")

func updateConfigMapIfNeeded(ctx context.Context, cl client.Client, log logger.Logger, configMap *corev1.ConfigMap, clusterID string, desiredClusterConfig *v1alpha1.ClusterConfig) error {
	log = log.Named("configmap").Named("update")

	log.Debug(fmt.Sprintf("starts for the ConfigMap %s/%s", configMap.Namespace, configMap.Name))
	log.Trace(fmt.Sprintf("ConfigMap: %+v", configMap))
	log.Trace(fmt.Sprintf("desired cluster config for the cluster %s: %+v", clusterID, desiredClusterConfig))

	if configMap.DeletionTimestamp != nil {
		// delete finalizers
		configMap.Finalizers = slices.DeleteFunc(
			configMap.Finalizers,
			func(f string) bool { return f == CephClusterConnectionControllerFinalizerName },
		)
		if err := cl.Update(ctx, configMap); err != nil {
			return err
		}
		return errUpdateIncompleted
	}

	needUpdate := false

	clusterConfigs, err := getClusterConfigsFromConfigMap(configMap)
	if err != nil {
		log.Warn(fmt.Sprintf("unable to get cluster configs from the ConfigMap %s. New cluster config will be created. Error: %s", configMap.Name, err.Error()))
		clusterConfigs = []v1alpha1.ClusterConfig{}
		needUpdate = true
	}

	log.Trace(fmt.Sprintf("clusterConfigs: %+v", clusterConfigs))

	clusterConfigs, updated := updateClusterConfigsIfNeeded(log, clusterConfigs, clusterID, desiredClusterConfig)
	if updated {
		needUpdate = true
	}

	obj, metadataAdded := addRequiredMetadataIfNeeded(configMap)
	configMap = obj.(*corev1.ConfigMap)
	if metadataAdded {
		needUpdate = true
	}

	if !needUpdate {
		log.Debug(fmt.Sprintf("no changes required for the ConfigMap %s", configMap.Name))
		return nil
	}

	log.Debug(fmt.Sprintf("changes required for the ConfigMap %s", configMap.Name))
	newJSONData, err := json.Marshal(clusterConfigs)
	if err != nil {
		return fmt.Errorf("[updateConfigMapIfNeeded] unable to marshal clusterConfigs: %w", err)
	}
	log.Trace(fmt.Sprintf("newJSONData: %s", newJSONData))

	if configMap.Data == nil {
		configMap.Data = map[string]string{}
	}

	if newJSONData == nil {
		return fmt.Errorf("[updateConfigMapIfNeeded] newJSONData is nil")
	}

	configMap.Data["config.json"] = string(newJSONData)

	log.Trace(fmt.Sprintf("updated ConfigMap: %+v", configMap))

	err = cl.Update(ctx, configMap)
	if err != nil {
		return fmt.Errorf("[updateConfigMapIfNeeded] unable to update the ConfigMap %s: %w", configMap.Name, err)
	}

	return nil
}

func findClusterConfigByClusterID(clusterConfigs []v1alpha1.ClusterConfig, clusterID string) (int, bool) {
	for i, clusterConfig := range clusterConfigs {
		if clusterConfig.ClusterID == clusterID {
			return i, true
		}
	}

	return -1, false
}

func updateClusterConfigsIfNeeded(log logger.Logger, clusterConfigs []v1alpha1.ClusterConfig, clusterID string, desiredClusterConfig *v1alpha1.ClusterConfig) ([]v1alpha1.ClusterConfig, bool) {
	log = log.Named("cluster-configs").Named("update")

	updated := false

	clusterConfigIndex, clusterConfigExists := findClusterConfigByClusterID(clusterConfigs, clusterID)
	log.Debug(fmt.Sprintf("find cluster config by cluster ID: %d, %t. Index: %d", clusterConfigIndex, clusterConfigExists, clusterConfigIndex))

	if desiredClusterConfig == nil {
		if clusterConfigExists {
			log.Debug(fmt.Sprintf("clusterConfigExists %t and no CephClusterConnection is left for it. Delete clusterConfig", clusterConfigExists))
			clusterConfigs = append(clusterConfigs[:clusterConfigIndex], clusterConfigs[clusterConfigIndex+1:]...)
			updated = true
		} else {
			log.Debug(fmt.Sprintf("clusterConfigExists %t. No need to delete clusterConfig", clusterConfigExists))
		}
	} else {
		newClusterConfig := *desiredClusterConfig
		log.Trace(fmt.Sprintf("newClusterConfig: %+v", newClusterConfig))

		if clusterConfigExists {
			log.Trace(fmt.Sprintf("existedClusterConfig: %+v", clusterConfigs[clusterConfigIndex]))
			if !reflect.DeepEqual(clusterConfigs[clusterConfigIndex], newClusterConfig) {
				log.Debug(fmt.Sprintf("clusterConfigExists: %t and configs differ. Updating clusterConfig at index %d", clusterConfigExists, clusterConfigIndex))
				clusterConfigs[clusterConfigIndex] = newClusterConfig
				updated = true
			} else {
				log.Debug(fmt.Sprintf("clusterConfigExists: %t and configs are equal. No need to update", clusterConfigExists))
			}
		} else {
			log.Debug(fmt.Sprintf("clusterConfigExists %t. Append newClusterConfig", clusterConfigExists))
			clusterConfigs = append(clusterConfigs, newClusterConfig)
			updated = true
		}
	}

	return clusterConfigs, updated
}

// Secret
func reconcileSecret(ctx context.Context, cl client.Client, log logger.Logger, secretList *corev1.SecretList, cephClusterConnection *v1alpha1.CephClusterConnection, usage volumeUsage, secretNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	var secret *corev1.Secret
	for _, s := range secretList.Items {
		if s.Name == secretName {
			secret = &s
			break
		}
	}

	if cephClusterConnection.DeletionTimestamp != nil {
		if inUse := usage.secretInUse(secretNamespace, secretName, cephClusterConnection.Name); inUse != nil {
			return true, inUse.Error(), inUse
		}
		return removeFinalizerAndDeleteSecret(ctx, cl, log, secret, CephClusterConnectionControllerFinalizerName)
	}

	if secret == nil {
		return createSecret(ctx, cl, log, cephClusterConnection, secretNamespace, secretName)
	}

	err = updateSecretIfNeeded(ctx, cl, log, secret, cephClusterConnection)
	if err != nil {
		return true, err.Error(), err
	}

	return false, fmt.Sprintf("Successfully reconciled Secret %s", secretName), nil
}

func createSecret(ctx context.Context, cl client.Client, log logger.Logger, cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) (shouldRequeue bool, msg string, err error) {
	log = log.Named("secret").Named("create")

	log.Debug(fmt.Sprintf("starts creation of Secret %s for CephClusterConnection %s", secretName, cephClusterConnection.Name))

	newSecret := generateNewSecret(cephClusterConnection, controllerNamespace, secretName)
	log.Debug(fmt.Sprintf("successfully generate the Secret %s for the CephClusterConnection %s", secretName, cephClusterConnection.Name))

	err = cl.Create(ctx, newSecret)
	if err != nil {
		err = fmt.Errorf("[createSecret] unable to create a Secret %s for CephClusterConnection %s: %w", newSecret.Name, cephClusterConnection.Name, err)
		return true, err.Error(), err
	}

	log.Debug(fmt.Sprintf("successfully created Secret %s for the CephClusterConnection %s", newSecret.Name, cephClusterConnection.Name))
	return false, fmt.Sprintf("Successfully created Secret %s", newSecret.Name), nil
}

func generateNewSecret(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace, secretName string) *corev1.Secret {
	userID := cephClusterConnection.Spec.UserID
	userKey := cephClusterConnection.Spec.UserKey

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: controllerNamespace,
			Labels: map[string]string{
				internal.StorageManagedLabelKey: CephClusterConnectionCtrlName,
			},
			Finalizers: []string{CephClusterConnectionControllerFinalizerName},
		},
		StringData: map[string]string{
			// Credentials for RBD
			"userID":  userID,
			"userKey": userKey,

			// Credentials for CephFS
			"adminID":  userID,
			"adminKey": userKey,
		},
	}

	return secret
}

// volumeUsage is the snapshot of the PersistentVolumes one reconcile pass
// works from, with the names of those a VolumeAttachment refers to, and of
// the VolumeSnapshotContents, none when their CRD is not installed.
type volumeUsage struct {
	pvs              []corev1.PersistentVolume
	attached         map[string]bool
	snapshotContents []snapshotv1.VolumeSnapshotContent
}

func listVolumeUsage(ctx context.Context, cl client.Reader) (volumeUsage, error) {
	pvList := &corev1.PersistentVolumeList{}
	if err := cl.List(ctx, pvList); err != nil {
		return volumeUsage{}, fmt.Errorf("[listVolumeUsage] unable to list PersistentVolumes: %w", err)
	}

	attachmentList := &storagev1.VolumeAttachmentList{}
	if err := cl.List(ctx, attachmentList); err != nil {
		return volumeUsage{}, fmt.Errorf("[listVolumeUsage] unable to list VolumeAttachments: %w", err)
	}

	attached := make(map[string]bool, len(attachmentList.Items))
	for _, attachment := range attachmentList.Items {
		if name := attachment.Spec.Source.PersistentVolumeName; name != nil {
			attached[*name] = true
		}
	}

	// The snapshot CRDs are optional (see VolumeSnapshotClassCRDExists).
	contentList := &snapshotv1.VolumeSnapshotContentList{}
	if err := cl.List(ctx, contentList); err != nil && !meta.IsNoMatchError(err) {
		return volumeUsage{}, fmt.Errorf("[listVolumeUsage] unable to list VolumeSnapshotContents: %w", err)
	}

	return volumeUsage{pvs: pvList.Items, attached: attached, snapshotContents: contentList.Items}, nil
}

// snapshotContentsNeedingSecret returns the sorted names of the
// VolumeSnapshotContents whose deletion secret annotations of the
// external-snapshotter name the secret and whose snapshot is deleted with
// them: the csi-snapshotter reads that secret to delete the snapshot, and a
// content whose secret is gone keeps its finalizer, and its VolumeSnapshot,
// for good.
func (u volumeUsage) snapshotContentsNeedingSecret(secretNamespace, secretName string) []string {
	var names []string
	for _, content := range u.snapshotContents {
		if content.Annotations[snapshotDeletionSecretNameAnnotation] == secretName &&
			content.Annotations[snapshotDeletionSecretNamespaceAnnotation] == secretNamespace &&
			content.Spec.DeletionPolicy == snapshotv1.VolumeSnapshotContentDelete {
			names = append(names, content.Name)
		}
	}

	slices.Sort(names)
	return names
}

// secretNeeded reports whether a PersistentVolume or a VolumeSnapshotContent
// still needs the secret.
func (u volumeUsage) secretNeeded(secretNamespace, secretName string) bool {
	return len(u.pvsNeedingSecret(secretNamespace, secretName)) > 0 || len(u.snapshotContentsNeedingSecret(secretNamespace, secretName)) > 0
}

// secretInUse returns the error that holds the deletion of the
// CephClusterConnection connectionName while PersistentVolumes or
// VolumeSnapshotContents still need its secret, or nil.
func (u volumeUsage) secretInUse(secretNamespace, secretName, connectionName string) *secretInUseError {
	pvNames := u.pvsNeedingSecret(secretNamespace, secretName)
	contentNames := u.snapshotContentsNeedingSecret(secretNamespace, secretName)
	if len(pvNames) == 0 && len(contentNames) == 0 {
		return nil
	}

	var users []string
	if len(pvNames) > 0 {
		users = append(users, fmt.Sprintf("%d PersistentVolumes %v", len(pvNames), listedNames(pvNames)))
	}
	if len(contentNames) > 0 {
		users = append(users, fmt.Sprintf("%d VolumeSnapshotContents %v", len(contentNames), listedNames(contentNames)))
	}
	return &secretInUseError{
		message:     fmt.Sprintf("the Secret %s/%s is still used by %s; the CephClusterConnection %s is deleted once they are gone", secretNamespace, secretName, strings.Join(users, " and "), connectionName),
		volumesHold: len(pvNames) > 0,
	}
}

// pvsNeedingSecret returns the sorted names of the PersistentVolumes that
// name the secret in any of their CSI secret references or in the deletion
// secret annotations of the external-provisioner, and that will still have it
// read:
//   - one a VolumeAttachment refers to: the external-attacher reads its
//     controller-publish secret to detach it, and a detach that cannot read
//     the secret never finishes;
//   - one Released or Failed with the Delete reclaim policy: the
//     external-provisioner reads its deletion secret to delete it;
//   - any other one, Bound or not claimed yet, which can still be attached,
//     staged, expanded or deleted.
//
// A Released or Failed volume that is kept and attached nowhere has no CSI
// call left that reads its secrets, so it does not hold the secret.
func (u volumeUsage) pvsNeedingSecret(secretNamespace, secretName string) []string {
	isSecret := func(ref *corev1.SecretReference) bool {
		return ref != nil && ref.Name == secretName && ref.Namespace == secretNamespace
	}

	var names []string
	for _, pv := range u.pvs {
		used := pv.Annotations[provisionerDeletionSecretNameAnnotation] == secretName && pv.Annotations[provisionerDeletionSecretNamespaceAnnotation] == secretNamespace
		if csi := pv.Spec.CSI; csi != nil {
			used = used ||
				isSecret(csi.ControllerPublishSecretRef) ||
				isSecret(csi.NodeStageSecretRef) ||
				isSecret(csi.NodePublishSecretRef) ||
				isSecret(csi.ControllerExpandSecretRef) ||
				isSecret(csi.NodeExpandSecretRef)
		}
		if used && u.needsSecrets(&pv) {
			names = append(names, pv.Name)
		}
	}

	slices.Sort(names)
	return names
}

// secretInUseError holds the deletion of a CephClusterConnection while
// PersistentVolumes or VolumeSnapshotContents still need its secret. It gives
// the Ready condition its own reason, which consumers such as sds-elastic read
// to tell this wait from a failure.
type secretInUseError struct {
	message string
	// volumesHold is false when only VolumeSnapshotContents need the secret.
	volumesHold bool
}

func (e *secretInUseError) Error() string { return e.message }

// reason returns the reason of the Ready condition for this wait.
func (e *secretInUseError) reason() string {
	if e.volumesHold {
		return v1alpha1.ReasonPersistentVolumesExist
	}
	return v1alpha1.ReasonVolumeSnapshotContentsExist
}

// listedNames returns the names a status message lists out of the sorted
// names.
func listedNames(names []string) []string {
	if len(names) > maxListedNames {
		return names[:maxListedNames]
	}
	return names
}

func (u volumeUsage) needsSecrets(pv *corev1.PersistentVolume) bool {
	if u.attached[pv.Name] {
		return true
	}
	switch pv.Status.Phase {
	case corev1.VolumeReleased, corev1.VolumeFailed:
		return pv.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimDelete
	default:
		return true
	}
}

func removeFinalizerAndDeleteSecret(ctx context.Context, cl client.Client, log logger.Logger, secret *corev1.Secret, finalizerName string) (shouldRequeue bool, msg string, err error) {
	if secret == nil {
		log.Debug("secret is nil. No need to delete")
		return false, "[deleteSecret] Secret is nil. No need to delete", nil
	}

	err = removeFinalizerIfExists(ctx, cl, secret, finalizerName)
	if err != nil {
		err = fmt.Errorf("[deleteSecret] unable to remove finalizer from the Secret %s: %w", secret.Name, err)
		return true, err.Error(), err
	}

	err = cl.Delete(ctx, secret)
	if err != nil {
		err = fmt.Errorf("[deleteSecret] unable to delete the Secret %s: %w", secret.Name, err)
		return true, err.Error(), err
	}

	log.Named("secret").Named("delete").Debug(fmt.Sprintf("successfully deleted Secret %s", secret.Name))

	return false, fmt.Sprintf("[deleteSecret] Successfully deleted Secret %s", secret.Name), nil
}

func updateSecretIfNeeded(ctx context.Context, cl client.Client, log logger.Logger, oldSecret *corev1.Secret, cephClusterConnection *v1alpha1.CephClusterConnection) error {
	log = log.Named("secret").Named("update")

	log.Debug(fmt.Sprintf("starts for the Secret %s/%s", oldSecret.Namespace, oldSecret.Name))

	needUpdate := false

	newSecret := generateNewSecret(cephClusterConnection, oldSecret.Namespace, oldSecret.Name)
	needUpdate = compareSecretsData(oldSecret, newSecret)
	if needUpdate {
		log.Debug(fmt.Sprintf("data in the Secret %s has been changed", oldSecret.Name))
		oldSecret.StringData = newSecret.StringData
	}

	obj, metadataAdded := addRequiredMetadataIfNeeded(oldSecret)
	oldSecret = obj.(*corev1.Secret)
	if metadataAdded {
		log.Trace(fmt.Sprintf("metadataAdded: %t", metadataAdded))
		needUpdate = true
	}

	if !needUpdate {
		log.Debug(fmt.Sprintf("no changes required for the Secret %s", oldSecret.Name))
		return nil
	}

	log.Debug(fmt.Sprintf("changes required for the Secret %s", oldSecret.Name))

	err := cl.Update(ctx, oldSecret)
	if err != nil {
		return fmt.Errorf("[updateSecretIfNeeded] unable to update the Secret %s: %w", oldSecret.Name, err)
	}

	return nil
}

func addRequiredMetadataIfNeeded(obj metav1.Object) (metav1.Object, bool) {
	labelsAdded := false
	finalizersAdded := false

	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		labelsAdded = true
	}

	if labels[internal.StorageManagedLabelKey] != CephClusterConnectionCtrlName {
		labels[internal.StorageManagedLabelKey] = CephClusterConnectionCtrlName
		labelsAdded = true
	}

	if labelsAdded {
		obj.SetLabels(labels)
	}

	finalizers := obj.GetFinalizers()
	if finalizers == nil {
		finalizers = []string{}
		finalizersAdded = true
	}

	if !slices.Contains(finalizers, CephClusterConnectionControllerFinalizerName) {
		finalizers = append(finalizers, CephClusterConnectionControllerFinalizerName)
		finalizersAdded = true
	}

	if finalizersAdded {
		obj.SetFinalizers(finalizers)
	}

	return obj, labelsAdded || finalizersAdded
}

func compareSecretsData(oldSecret, newSecret *corev1.Secret) bool {
	if len(newSecret.StringData) != len(oldSecret.Data) {
		return true
	}

	for key, newValue := range newSecret.StringData {
		oldValBytes, found := oldSecret.Data[key]
		if !found {
			return true
		}

		oldValue := string(oldValBytes)
		if newValue != oldValue {
			return true
		}
	}

	return false
}

// GenerateClusterConfigForTesting is a wrapper for testing the generateClusterConfig function
func GenerateClusterConfigForTesting(cephClusterConnection *v1alpha1.CephClusterConnection, controllerNamespace string) v1alpha1.ClusterConfig {
	return generateClusterConfig(cephClusterConnection, controllerNamespace)
}
