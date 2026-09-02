#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SSH_PRIVATE_KEY=${SSH_PRIVATE_KEY:-}
METALLB_ADDRESS_POOL=${METALLB_ADDRESS_POOL:-}
KUBECONFIG_PATH=${KUBECONFIG_PATH:-"$ROOT_DIR/kubeconfig"}

if [[ -z "$SSH_PRIVATE_KEY" ]]; then
  echo "SSH_PRIVATE_KEY must point to the private key matching ssh_public_key." >&2
  exit 1
fi
if [[ ! -f "$SSH_PRIVATE_KEY" ]]; then
  echo "SSH private key not found: $SSH_PRIVATE_KEY" >&2
  exit 1
fi

for command in terraform jq ssh; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "Required command not found: $command" >&2
    exit 1
  fi
done

cd "$ROOT_DIR"
node_addresses=$(terraform output -json node_addresses)
primary_name=$(terraform output -json primary_node | jq -r '.name')
primary_address=$(terraform output -json primary_node | jq -r '.address')
ssh_username=$(terraform output -raw ssh_username)

ssh_options=(
  -i "$SSH_PRIVATE_KEY"
  -o BatchMode=yes
  -o ConnectTimeout=10
  -o ServerAliveInterval=15
  -o StrictHostKeyChecking=accept-new
)

remote() {
  local address=$1
  shift
  # Arguments are intentionally expanded locally into the remote command.
  # shellcheck disable=SC2029
  ssh "${ssh_options[@]}" "$ssh_username@$address" "$@"
}

echo "Waiting for cloud-init on all nodes..."
while IFS=$'\t' read -r name address; do
  printf '  %-24s %s\n' "$name" "$address"
  until remote "$address" true >/dev/null 2>&1; do
    sleep 5
  done
  remote "$address" "sudo cloud-init status --wait && sudo timeout 600 microk8s status --wait-ready"
done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"$node_addresses")

echo "Initializing cluster from $primary_name ($primary_address)..."
while IFS=$'\t' read -r name address; do
  [[ "$name" == "$primary_name" ]] && continue

  if remote "$primary_address" "sudo microk8s kubectl get node '$name'" >/dev/null 2>&1; then
    echo "  $name is already joined"
    continue
  fi

  join_output=$(remote "$primary_address" "sudo microk8s add-node --token-ttl 3600")
  join_command=$(grep -E "microk8s join ${primary_address}:25000/" <<<"$join_output" | head -1 || true)
  if [[ -z "$join_command" ]]; then
    echo "Could not find a join command for $primary_address in microk8s add-node output:" >&2
    echo "$join_output" >&2
    exit 1
  fi

  echo "  joining $name ($address)"
  remote "$address" "sudo $join_command"
done < <(jq -r 'to_entries[] | [.key, .value] | @tsv' <<<"$node_addresses")

remote "$primary_address" "sudo microk8s kubectl wait --for=condition=Ready nodes --all --timeout=10m"

echo "Enabling DNS, hostpath storage, and Helm..."
remote "$primary_address" "sudo microk8s enable dns hostpath-storage helm3"

if [[ -n "$METALLB_ADDRESS_POOL" ]]; then
  echo "Enabling MetalLB with pool $METALLB_ADDRESS_POOL..."
  remote "$primary_address" "sudo microk8s enable metallb:$METALLB_ADDRESS_POOL"
else
  echo "METALLB_ADDRESS_POOL is unset; MetalLB was not enabled."
fi

remote "$primary_address" "sudo microk8s config" >"$KUBECONFIG_PATH"
chmod 0600 "$KUBECONFIG_PATH"

echo
remote "$primary_address" "sudo microk8s status"
echo
KUBECONFIG="$KUBECONFIG_PATH" kubectl get nodes -o wide 2>/dev/null || \
  remote "$primary_address" "sudo microk8s kubectl get nodes -o wide"
echo
echo "Kubeconfig written to $KUBECONFIG_PATH"
echo "Use it with: export KUBECONFIG=$KUBECONFIG_PATH"
