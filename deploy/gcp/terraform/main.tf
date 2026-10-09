locals {
  labels = { app = "agentrun-operator", purpose = "devfest-demo" }
}

resource "google_project_service" "apis" {
  for_each = toset([
    "compute.googleapis.com",
    "container.googleapis.com",
    "artifactregistry.googleapis.com",
    "iam.googleapis.com",
  ])
  service            = each.value
  disable_on_destroy = false
}

# ---------- network ----------

resource "google_compute_network" "vpc" {
  name                    = "${var.cluster_name}-vpc"
  auto_create_subnetworks = false
  depends_on              = [google_project_service.apis]
}

resource "google_compute_subnetwork" "nodes" {
  name                     = "${var.cluster_name}-nodes"
  network                  = google_compute_network.vpc.id
  region                   = var.region
  ip_cidr_range            = "10.10.0.0/20"
  private_ip_google_access = true

  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = "10.20.0.0/16"
  }
  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = "10.30.0.0/20"
  }
}

# ---------- images ----------

resource "google_artifact_registry_repository" "images" {
  repository_id = "agentrun"
  location      = var.region
  format        = "DOCKER"
  description   = "agentrun-operator images (controller + agent)"
  labels        = local.labels
  depends_on    = [google_project_service.apis]
}

# ---------- least-privilege identity for the nodes ----------

resource "google_service_account" "nodes" {
  account_id   = "${var.cluster_name}-nodes"
  display_name = "agentrun demo GKE nodes"
  depends_on   = [google_project_service.apis]
}

resource "google_project_iam_member" "nodes" {
  for_each = toset([
    "roles/artifactregistry.reader",
    "roles/logging.logWriter",
    "roles/monitoring.metricWriter",
    "roles/monitoring.viewer",
    "roles/stackdriver.resourceMetadata.writer",
  ])
  project = var.project_id
  role    = each.value
  member  = "serviceAccount:${google_service_account.nodes.email}"
}

# ---------- cluster ----------

resource "google_container_cluster" "demo" {
  name     = var.cluster_name
  location = var.zone

  network    = google_compute_network.vpc.id
  subnetwork = google_compute_subnetwork.nodes.id
  ip_allocation_policy {
    cluster_secondary_range_name  = "pods"
    services_secondary_range_name = "services"
  }

  # Dataplane V2 (Cilium/eBPF): NetworkPolicy is enforced out of the box.
  datapath_provider = "ADVANCED_DATAPATH"

  release_channel {
    channel = "REGULAR"
  }
  workload_identity_config {
    workload_pool = "${var.project_id}.svc.id.goog"
  }

  remove_default_node_pool = true
  initial_node_count       = 1
  deletion_protection      = false
  resource_labels          = local.labels

  depends_on = [google_project_service.apis]
}

# System pods (and the controller) run here.
resource "google_container_node_pool" "system" {
  name       = "system"
  cluster    = google_container_cluster.demo.id
  node_count = 1

  node_config {
    machine_type    = var.machine_type
    disk_type       = "pd-balanced"
    disk_size_gb    = 30
    image_type      = "COS_CONTAINERD"
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    labels          = local.labels
    workload_metadata_config {
      mode = "GKE_METADATA"
    }
    shielded_instance_config {
      enable_secure_boot = true
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# Every agent attempt runs here, inside gVisor (runtimeClassName: gvisor).
resource "google_container_node_pool" "sandbox" {
  name    = "gvisor"
  cluster = google_container_cluster.demo.id

  initial_node_count = 1
  autoscaling {
    min_node_count = 1
    max_node_count = var.sandbox_max_nodes
  }

  node_config {
    machine_type    = var.machine_type
    disk_type       = "pd-balanced"
    disk_size_gb    = 30
    image_type      = "COS_CONTAINERD"
    service_account = google_service_account.nodes.email
    oauth_scopes    = ["https://www.googleapis.com/auth/cloud-platform"]
    labels          = local.labels
    sandbox_config {
      type = "GVISOR"
    }
    workload_metadata_config {
      mode = "GKE_METADATA"
    }
    shielded_instance_config {
      enable_secure_boot = true
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}
