output "cluster_name" {
  value = google_container_cluster.demo.name
}

output "location" {
  value = google_container_cluster.demo.location
}

output "registry" {
  description = "Push images here."
  value       = "${var.region}-docker.pkg.dev/${var.project_id}/${google_artifact_registry_repository.images.repository_id}"
}

output "kube_context" {
  description = "Context name written by `gcloud container clusters get-credentials`."
  value       = "gke_${var.project_id}_${google_container_cluster.demo.location}_${google_container_cluster.demo.name}"
}

output "get_credentials" {
  value = "gcloud container clusters get-credentials ${google_container_cluster.demo.name} --zone ${google_container_cluster.demo.location} --project ${var.project_id}"
}
