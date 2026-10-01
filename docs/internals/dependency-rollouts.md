# Dependency rollouts

Setup stamps `mcpruntime.org/dependency-revision` on Deployment, StatefulSet,
DaemonSet and Job pod templates from the values they consume. It covers regular
and init-container env key references, envFrom, Secret/ConfigMap volumes and
projected sources. Individual references hash only their keys; whole-object
references hash all data. Identity, key presence and values are framed and hashed
in memory. Only the digest is stored; credential values are never logged.

Resources declared in the same manifest are evaluated from rendered data before
creation. Other resources are read once per manifest. Missing required resources
or keys and authorization errors fail planning; only optional NotFound is allowed.
Applying the unchanged manifest preserves its revision. A changed dependency
updates only affected templates and uses their normal rollout policies. The old
blanket restart of Sentinel deployments is removed. The first upgrade adopts the
annotation and may roll existing consumers once.

This covers supported setup apply paths. Standalone rotation commands must use
the same planner in follow-up integration, and live checks must verify unrelated
pod-template generations stay unchanged. Store password synchronization remains
required; a pod restart alone does not change persisted Grafana/Postgres passwords.
