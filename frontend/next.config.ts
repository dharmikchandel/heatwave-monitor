import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  // Emits a minimal .next/standalone server (no node_modules needed at
  // runtime) so the Docker image stays small — see Dockerfile.
  output: "standalone",
};

export default nextConfig;
