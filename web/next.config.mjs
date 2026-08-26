/** @type {import('next').NextConfig} */
const nextConfig = {
  // Static export: the Go binary embeds and serves the result, so the whole
  // product ships as a single container with no Node runtime in production.
  output: "export",
  images: { unoptimized: true },
  reactStrictMode: true,
  trailingSlash: false,
};

export default nextConfig;
