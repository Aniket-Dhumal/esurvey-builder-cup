import type { MetadataRoute } from "next";

/**
 * The web app manifest, emitted by the framework rather than hand-written into
 * public/ — this is the App Router convention and it is typed, so a field that
 * stops being valid is a build error rather than a manifest a phone silently
 * ignores.
 *
 * It exists so a farmer can add eSurvey to their home screen and have it open
 * like an app: full screen, portrait, straight into their own page. That
 * matters more than it sounds. A bookmark in a browser with a URL bar reads as
 * a website you visit; an icon on the home screen reads as a thing you own,
 * and it is the difference between filing a claim and meaning to.
 */
// A fully static export has no request to vary on, so the route has to say so
// explicitly or the build refuses to collect it.
export const dynamic = "force-static";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "eSurvey — पीक नुकसान",
    short_name: "eSurvey",
    description: "आपल्या शेतातील नुकसान कळवा · Report crop damage from your own field.",
    // Straight to the farmer's screen. The officer console is a different
    // product on a different device and is not what gets installed.
    start_url: "/my",
    scope: "/",
    display: "standalone",
    orientation: "portrait",
    background_color: "#ffffff",
    theme_color: "#1f7a3f",
    lang: "mr",
    icons: [
      {
        src: "/icon.svg",
        sizes: "any",
        type: "image/svg+xml",
        purpose: "maskable",
      },
    ],
  };
}
