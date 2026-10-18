import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  /**
   * Fully static export: every route is prerendered to HTML at build time and
   * all data fetching happens in the browser against the Go API. There is no
   * Node runtime in production — the output is a directory of files that any
   * static host, or the Go server itself, can serve.
   *
   * This is why the detail routes take their id from a query string rather
   * than a path segment: a static export cannot generate pages for ids that do
   * not exist until an officer creates them.
   */
  output: "export",

  // No next/image loader exists in a static export; the overlays are inline
  // data URIs anyway and are rendered with a plain <img>.
  images: { unoptimized: true },

  // Emit /assessment/index.html rather than /assessment.html so plain file
  // hosts resolve the route without rewrite rules.
  trailingSlash: true,
};

export default nextConfig;
