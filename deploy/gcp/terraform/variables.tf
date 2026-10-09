variable "project_id" {
  description = "GCP project that will hold the demo. Use a dedicated one: `make gke-down` destroys everything in this stack."
  type        = string
}

variable "region" {
  description = "Region for the network and the Artifact Registry repository."
  type        = string
  default     = "europe-west1"
}

variable "zone" {
  description = "Zone of the (zonal, cheapest) GKE cluster."
  type        = string
  default     = "europe-west1-b"
}

variable "cluster_name" {
  description = "Also the suffix the Makefile uses to recognise this cluster's kube context as safe."
  type        = string
  default     = "agentrun-demo"
}

variable "machine_type" {
  description = "Node machine type. GKE Sandbox needs a non-shared-core type (e2-standard-2 or larger)."
  type        = string
  default     = "e2-standard-2"
}

variable "sandbox_max_nodes" {
  description = "Upper bound for the gVisor node pool autoscaler."
  type        = number
  default     = 2
}
