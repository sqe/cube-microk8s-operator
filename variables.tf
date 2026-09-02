variable "proxmox_endpoint" {
  description = "Proxmox VE API URL, for example https://pve.example.com:8006/."
  type        = string
}

variable "proxmox_api_token" {
  description = "Proxmox API token in user@realm!token=secret format. Prefer TF_VAR_proxmox_api_token."
  type        = string
  sensitive   = true
}

variable "proxmox_insecure" {
  description = "Allow an untrusted Proxmox TLS certificate. Use false with a trusted certificate."
  type        = bool
  default     = false
}

variable "template_vm_id" {
  description = "VM ID of an Ubuntu 24.04 cloud-init template with a SCSI boot disk."
  type        = number
}

variable "template_node_name" {
  description = "Proxmox node that owns the source VM template."
  type        = string
}

variable "nodes" {
  description = "MicroK8s nodes keyed by their desired hostname. Addresses must include CIDR prefixes."
  type = map(object({
    proxmox_node = string
    vm_id        = number
    ipv4_address = string
  }))

  validation {
    condition     = length(var.nodes) >= 3
    error_message = "At least three nodes are required for MicroK8s high availability."
  }

  validation {
    condition     = length(distinct([for node in values(var.nodes) : node.vm_id])) == length(var.nodes)
    error_message = "Every node must have a unique Proxmox VM ID."
  }
}

variable "ipv4_gateway" {
  description = "Default IPv4 gateway for the MicroK8s nodes."
  type        = string
}

variable "dns_servers" {
  description = "DNS resolvers supplied through cloud-init."
  type        = list(string)
  default     = ["1.1.1.1", "1.0.0.1"]
}

variable "network_bridge" {
  description = "Proxmox Linux bridge attached to the VM network interfaces."
  type        = string
  default     = "vmbr0"
}

variable "network_vlan_id" {
  description = "Optional VLAN ID. Leave null for an untagged interface."
  type        = number
  default     = null
}

variable "vm_datastore_id" {
  description = "Datastore for cloned VM disks and cloud-init disks."
  type        = string
  default     = "local-lvm"
}

variable "snippets_datastore_id" {
  description = "Proxmox datastore with snippets content enabled."
  type        = string
  default     = "local"
}

variable "vm_cpu_cores" {
  description = "CPU cores per MicroK8s VM."
  type        = number
  default     = 4

  validation {
    condition     = var.vm_cpu_cores >= 2
    error_message = "Each node must have at least two CPU cores."
  }
}

variable "vm_memory_mb" {
  description = "Dedicated RAM per MicroK8s VM. Cube production components need substantial memory."
  type        = number
  default     = 16384

  validation {
    condition     = var.vm_memory_mb >= 8192
    error_message = "Each node must have at least 8192 MiB of RAM."
  }
}

variable "vm_disk_size_gb" {
  description = "Boot disk size per VM in GiB."
  type        = number
  default     = 100

  validation {
    condition     = var.vm_disk_size_gb >= 40
    error_message = "Each node disk must be at least 40 GiB."
  }
}

variable "vm_cpu_type" {
  description = "CPU model exposed to guests. Use host only when migration compatibility is not required."
  type        = string
  default     = "x86-64-v2-AES"
}

variable "cloud_init_username" {
  description = "Administrative Linux user created by cloud-init."
  type        = string
  default     = "ubuntu"
}

variable "ssh_public_key" {
  description = "OpenSSH public key authorized for the cloud-init user."
  type        = string
}

variable "microk8s_channel" {
  description = "Snap channel used to install MicroK8s."
  type        = string
  default     = "1.34/stable"
}

variable "protect_vms" {
  description = "Enable Proxmox deletion protection. Set false and apply before intentionally destroying VMs."
  type        = bool
  default     = true
}
