provider "proxmox" {
  endpoint  = var.proxmox_endpoint
  api_token = var.proxmox_api_token
  insecure  = var.proxmox_insecure
}

locals {
  node_names = sort(keys(var.nodes))
}

resource "proxmox_virtual_environment_file" "cloud_config" {
  for_each = var.nodes

  content_type = "snippets"
  datastore_id = var.snippets_datastore_id
  node_name    = each.value.proxmox_node

  source_raw {
    data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
      hostname         = each.key
      microk8s_channel = var.microk8s_channel
      username         = var.cloud_init_username
      ssh_public_key   = trimspace(var.ssh_public_key)
    })
    file_name = "${each.key}-cloud-init.yaml"
  }
}

resource "proxmox_virtual_environment_vm" "microk8s" {
  for_each = var.nodes

  name        = each.key
  description = "MicroK8s node for Cube Core; managed by Terraform"
  tags        = ["cube", "microk8s", "terraform"]
  node_name   = each.value.proxmox_node
  vm_id       = each.value.vm_id

  started         = true
  on_boot         = true
  protection      = var.protect_vms
  stop_on_destroy = true

  clone {
    vm_id        = var.template_vm_id
    node_name    = var.template_node_name
    datastore_id = var.vm_datastore_id
    full         = true
    retries      = 3
  }

  agent {
    enabled = true
    trim    = true

    wait_for_ip {
      ipv4 = true
    }
  }

  cpu {
    cores = var.vm_cpu_cores
    type  = var.vm_cpu_type
  }

  memory {
    dedicated = var.vm_memory_mb
  }

  disk {
    datastore_id = var.vm_datastore_id
    interface    = "scsi0"
    size         = var.vm_disk_size_gb
    discard      = "on"
    iothread     = true
    ssd          = true
  }

  initialization {
    datastore_id      = var.vm_datastore_id
    user_data_file_id = proxmox_virtual_environment_file.cloud_config[each.key].id

    dns {
      servers = var.dns_servers
    }

    ip_config {
      ipv4 {
        address = each.value.ipv4_address
        gateway = var.ipv4_gateway
      }
    }
  }

  network_device {
    bridge  = var.network_bridge
    model   = "virtio"
    vlan_id = var.network_vlan_id
  }

  operating_system {
    type = "l26"
  }

  serial_device {}

  startup {
    order      = tostring(index(local.node_names, each.key) + 1)
    up_delay   = "30"
    down_delay = "30"
  }

  lifecycle {
    precondition {
      condition     = can(cidrhost(each.value.ipv4_address, 0))
      error_message = "Node ${each.key} must have an IPv4 address in CIDR notation."
    }
  }
}
