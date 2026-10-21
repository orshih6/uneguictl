# Deploying the watch bot

`uneguictl watch` runs on the prod cluster as Deployment `uneguictl` in namespace
`uneguictl`, **deployed by Argo CD from `orshih6/manifest` `apps/uneguictl`**: a push to
`main` builds `ghcr.io/orshih6/uneguictl:sha-<commit>` and CI points that folder at it.
Change the Deployment there, not with `kubectl` (Argo reverts it).

Hand-applied from here: the Namespace, the PVC holding the state file, and the Secrets.

```bash
export KUBECONFIG=/Users/ori/myinternal/k0s/kubeconfig.yaml
kubectl apply -f k8s/namespace.yaml -f k8s/pvc.yaml

# Copy the GHCR pull secret from an existing namespace.
kubectl get secret ghcr-pull-secret -n dms-admin -o json \
  | jq 'del(.metadata.namespace,.metadata.uid,.metadata.resourceVersion,.metadata.creationTimestamp,.metadata.ownerReferences)' \
  | kubectl apply -n uneguictl -f -

kubectl create secret generic uneguictl-secrets -n uneguictl \
  --from-literal=TELEGRAM_BOT_TOKEN='<token of a bot used only by uneguictl>' \
  --from-literal=TELEGRAM_CHAT_ID='<your chat id>'
```

The bot is **@UneguimnOriBot**; its token is `UNEGUI_BOT_TOKEN` in the workspace `.env`.
Use a bot of its own: the watch long-polls `getUpdates`, and two programs polling one bot
steal each other's messages. Telegram lets a bot message you only after you have sent it
`/start`.

```bash
kubectl logs -n uneguictl deploy/uneguictl -f
```

## Claude: the connector and the chat

The bot serves an MCP connector on port 8790 (`--connector`, Service `uneguictl-connector`):
unegui.mn lookups and the rules, no phone tool. Only claudeweb's api pod may reach it
(`k8s/networkpolicy.yaml` here, and the matching egress rule in claudeweb's
`k8s/networkpolicy.yaml`, which also lets this bot call claudeweb's API). Telegram messages that
are not commands go to a claudeweb chat (Sonnet) with the connector on.

```bash
kubectl apply -f k8s/networkpolicy.yaml        # and claudeweb/k8s/networkpolicy.yaml
TOKEN=$(openssl rand -hex 32)
CW=$(kubectl -n claudeweb get secret claudeweb-api -o jsonpath='{.data.CLAUDEWEB_API_TOKEN}' | base64 -d)
kubectl -n uneguictl patch secret uneguictl-secrets --type merge --patch-file \
  <(printf '{"stringData":{"UNEGUICTL_CONNECTOR_TOKEN":"%s","CLAUDEWEB_API_TOKEN":"%s"}}' "$TOKEN" "$CW")
```

Then register the connector in claudeweb as `unegui` (Settings → Connectors, or its API):
transport `http`, URL `http://uneguictl-connector.uneguictl.svc:8790/mcp`, header
`Authorization: Bearer $TOKEN`. Then `unset TOKEN CW`.

Rules live in the state file (`/data/watch.json` on the PVC) and are managed from Telegram
with `/add`, `/rules`, `/pause`, `/remove`; see the README.
