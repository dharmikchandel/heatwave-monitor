// CI/CD pipeline for Heatwave Monitor — DevOps Experiment 5.
// Checkout -> Install -> Build -> Test -> Docker Build -> Kubernetes Deploy.
// Requires: git, bun, docker, kubectl on the Jenkins agent (see README.md).
pipeline {
    agent any

    environment {
        IMAGE_NAME = 'heatwave-monitor'
        IMAGE_TAG  = "${env.BUILD_NUMBER}"
    }

    stages {
        stage('Checkout') {
            steps {
                echo 'Checking out source from GitHub...'
                checkout scm
            }
        }

        stage('Install Dependencies') {
            steps {
                echo 'Installing dependencies with Bun...'
                sh 'bun install --frozen-lockfile'
            }
        }

        stage('Build') {
            steps {
                echo 'Building the Next.js production bundle...'
                sh 'bun run build'
            }
        }

        stage('Test') {
            steps {
                echo 'Running the automated test suite (bun test)...'
                sh 'bun test'
            }
        }

        stage('Docker Build') {
            steps {
                echo "Building Docker image ${IMAGE_NAME}:${IMAGE_TAG}..."
                sh "docker build -t ${IMAGE_NAME}:${IMAGE_TAG} -t ${IMAGE_NAME}:latest ."
            }
        }

        stage('Kubernetes Deploy') {
            steps {
                echo 'Applying Kubernetes manifests...'
                sh 'kubectl apply -f k8s/'
                echo "Rolling out image ${IMAGE_NAME}:${IMAGE_TAG}..."
                sh "kubectl set image deployment/heatwave-monitor heatwave-monitor=${IMAGE_NAME}:${IMAGE_TAG}"
                sh 'kubectl rollout status deployment/heatwave-monitor --timeout=120s'
            }
        }

        stage('Verify Deployment') {
            steps {
                echo 'Verifying Kubernetes resources...'
                sh 'kubectl get deployment heatwave-monitor'
                sh 'kubectl get pods -l app=heatwave-monitor'
                sh 'kubectl get service heatwave-monitor'
            }
        }
    }

    post {
        success {
            echo 'Pipeline succeeded — archiving build artifact.'
            sh "printf 'build: %s\\ngit_commit: %s\\nimage: %s:%s\\n' \"${env.BUILD_NUMBER}\" \"${env.GIT_COMMIT}\" \"${IMAGE_NAME}\" \"${IMAGE_TAG}\" > build-info.txt"
            archiveArtifacts artifacts: 'package.json, build-info.txt', fingerprint: true
        }
        failure {
            echo 'Pipeline failed — check the stage logs above for details.'
        }
    }
}
