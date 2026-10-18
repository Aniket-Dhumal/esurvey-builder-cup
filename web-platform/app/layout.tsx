import type { Metadata } from "next";
import "./globals.css";
import { AppShell } from "@/components/AppShell";
import { SessionProvider } from "@/components/SessionProvider";
import { TooltipProvider } from "@/components/ui/tooltip";
import { IBM_Plex_Mono, Inter, Noto_Sans_Devanagari } from "next/font/google";
import { cn } from "@/lib/utils";

/**
 * The typeface, and why it is a dull one.
 *
 * # What was wrong with the last choice
 *
 * Poppins. It is a geometric display face — perfectly circular bowls, a
 * single-storey a, generous width — and it was chosen here for one good reason
 * (it carries Devanagari) and no others. Geometric faces are drawn to be
 * noticed, which is right on a poster and wrong on a screen an officer reads
 * for a whole shift. It made a compensation console look like a landing page.
 *
 * Inter is the opposite kind of decision: drawn for user interfaces, tall
 * x-height, unremarkable, and it disappears behind the data. That is the whole
 * job.
 *
 * # Why three families rather than one
 *
 * No single well-drawn UI face covers Latin and Devanagari, so the stack names
 * both and the browser picks per glyph: Inter for the Latin, Noto Sans
 * Devanagari for the Marathi. Noto is what most Indian government systems
 * already render Devanagari with, so the console will look like its
 * neighbours rather than like a product.
 *
 * IBM Plex Mono is for reference numbers. They were on a bare system stack —
 * ui-monospace through to Consolas — so CLR-2026-496-00001 rendered in
 * whatever the operating system happened to have, matched to nothing else on
 * the page. A reference is read digit by digit and compared down a column, so
 * it wants a real monospace with disambiguated zeros, loaded rather than
 * hoped for.
 */
const inter = Inter({
  subsets: ["latin"],
  variable: "--font-inter",
  display: "swap",
});

const devanagari = Noto_Sans_Devanagari({
  subsets: ["devanagari"],
  weight: ["400", "500", "600", "700"],
  variable: "--font-devanagari",
  display: "swap",
});

const mono = IBM_Plex_Mono({
  subsets: ["latin"],
  weight: ["400", "500", "600"],
  variable: "--font-mono-face",
  display: "swap",
});

export const metadata: Metadata = {
  title: "eSurvey — Post Disaster Crop Loss Assessment Platform",
  description:
    "Post Disaster Crop Loss Assessment Platform. Satellite, radar, terrain and field survey evidence, assessed and verified up the revenue chain.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html
      lang="en"
      className={cn(
        "font-sans",
        inter.variable,
        devanagari.variable,
        mono.variable,
      )}
    >
      {/* suppressHydrationWarning on the body element only.
       *
       * Browser extensions write attributes onto <body> before React loads --
       * cz-shortcut-listen from ColorZilla, and others like it -- and the
       * server HTML cannot have them, so every page load logged a hydration
       * mismatch that had nothing to do with this application. This silences
       * that comparison for this element's attributes and nothing else: a
       * genuine mismatch inside the app still reports, because the flag does
       * not descend to children. */}
      <body className="antialiased" suppressHydrationWarning>
        <SessionProvider>
          <TooltipProvider delayDuration={200}>
            <AppShell>{children}</AppShell>
          </TooltipProvider>
        </SessionProvider>
      </body>
    </html>
  );
}
