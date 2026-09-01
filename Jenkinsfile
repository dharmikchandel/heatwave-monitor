// CI/CD pipeline for Heatwave Monitor — DevOps Experiment 5.
// Checkout -> Install -> Build -> Test -> Docker Build -> Kubernetes Deploy.
// Requires: git, bun, docker, kubectl on the Jenkins agent (see README.md).
// Node.js 20 is installed into the workspace so Next.js 16 builds do not depend on the host Node version.
pipeline {
    agent any

    environment {
        IMAGE_NAME = 'heatwave-monitor'
        IMAGE_TAG  = "${env.BUILD_NUMBER}"
        NODE_VERSION = '20.19.5'
    }

    stages {
        stage('Checkout') {
            steps {
                echo 'Checking out source from GitHub...'
                checkout scm
            }
        }

        stage('Prepare Node.js') {
            steps {
                echo "Preparing Node.js ${NODE_VERSION} for Next.js..."
                script {
                    def nodeArch = sh(
                        script: '''
                            set -eu
                            case "$(uname -m)" in
                                x86_64) echo "x64" ;;
                                aarch64|arm64) echo "arm64" ;;
                                *) echo "Unsupported CPU architecture: $(uname -m)" >&2; exit 1 ;;
                            esac
                        ''',
                        returnStdout: true
                    ).trim()

                    env.NODE_DIST = "node-v${env.NODE_VERSION}-linux-${nodeArch}"
                    env.NODE_HOME = "${env.WORKSPACE}/.jenkins-tools/${env.NODE_DIST}"
                    env.PATH = "${env.NODE_HOME}/bin:${env.PATH}"
                }
                sh '''
                    set -eu

                    if [ ! -x "${NODE_HOME}/bin/node" ]; then
                        mkdir -p "${WORKSPACE}/.jenkins-tools"
                        curl -fsSLO "https://nodejs.org/dist/v${NODE_VERSION}/${NODE_DIST}.tar.xz"
                        tar -xJf "${NODE_DIST}.tar.xz" -C "${WORKSPACE}/.jenkins-tools"
                        rm -f "${NODE_DIST}.tar.xz"
                    fi

                    node --version
                    npm --version
                '''
            }
        }

        stage('Install Dependencies') {
            steps {
                echo 'Installing dependencies with Bun...'
                sh 'node --version'
                sh 'bun install --frozen-lockfile'
            }
        }

        stage('Build') {
            steps {
                echo 'Building the Next.js production bundle...'
                sh 'node --version'
                sh 'bun run build'
            }
        }

        stage('Test') {
            steps {
                echo 'Running the automated test suite (bun test)...'
                sh 'node --version'
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
