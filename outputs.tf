output "node_addresses" {
  description = "Static IPv4 addresses by MicroK8s node name."
  value = {
    for name, node in var.nodes : name => split("/", node.ipv4_address)[0]
  }
}

output "node_vm_ids" {
  description = "Proxmox VM IDs by MicroK8s node name."
  value = {
    for name, vm in proxmox_virtual_environment_vm.microk8s : name => vm.vm_id
  }
}

output "primary_node" {
  description = "Node used to initialize the MicroK8s cluster."
  value = {
    name    = local.node_names[0]
    address = split("/", var.nodes[local.node_names[0]].ipv4_address)[0]
  }
}

output "ssh_username" {
  description = "Linux user expected by the bootstrap script."
  value       = var.cloud_init_username
}

output "bootstrap_command" {
  description = "Run after terraform apply and cloud-init completion."
  value       = "SSH_PRIVATE_KEY=~/.ssh/id_ed25519 METALLB_ADDRESS_POOL=192.168.1.200-192.168.1.220 ./scripts/bootstrap-microk8s.sh"
}
