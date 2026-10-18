import Image from "next/image";

/**
 * The eSurvey brand.
 *
 * # Two kinds of asset, treated differently
 *
 * The logo and the roundel are the supplied artwork and stay exactly that.
 * They are small, they are used at a size the files support, and they are the
 * one thing here that has to be itself rather than a close approximation —
 * a redrawn wordmark is a different wordmark however good the tracing.
 *
 * The background is the opposite. The supplied version is 600x400 with the
 * wordmark and four captions burned into the pixels, and across a modern
 * display that is a 3x upscale. A photograph tolerates that — softness reads
 * as depth of field — but type does not, and the captions came out as mush.
 * So the scene is drawn: exact at any resolution, nothing to download, and its
 * greens come from the palette rather than from a JPEG that happens to sit
 * near it.
 */

/**
 * The lockup, from the supplied artwork.
 *
 * The logo is a raster and stays one. It is small, it is drawn at 3x, and it
 * is the one thing on the page that must be exactly itself rather than an
 * approximation of itself — a redrawn wordmark is a different wordmark, however
 * close the traces get.
 *
 * The background is the opposite case, and is drawn: see FieldScene below.
 */
export function Wordmark({
  className,
  width = 340,
}: {
  className?: string;
  width?: number;
}) {
  return (
    <Image
      src="/brand/logo-esurvey@3x.png"
      alt="eSurvey — Survey today, secure tomorrow"
      width={1131}
      height={426}
      className={className}
      style={{ width, height: "auto" }}
      priority
  />
  );
}

/** The roundel, for places with room for a glyph and not a name. */
export function Mark({ className }: { className?: string }) {
  return (
    <Image
      src="/brand/icon-esurvey.png"
      alt=""
      width={127}
      height={97}
      className={className}
      priority
  />
  );
}

/**
 * Sunrise over a planted field.
 *
 * # Why the first attempt looked like a child drew it
 *
 * It was made of shapes: a rectangle for the field, stripes for the rows,
 * ellipses for the trees. Nothing in a landscape is a shape at that scale and
 * the eye knows it immediately. Three things fix it, and none of them is more
 * detail for its own sake.
 *
 * *Texture instead of stripes.* The field is three hundred-odd individual crop
 * plants standing in rows, not a green rectangle with lines drawn on it. A
 * photograph of a field reads as a field because of the repeating tuft, and the
 * cheapest way to get that is to draw the tuft repeatedly.
 *
 * *One projection, applied to everything.* A plant's size, its row's spacing
 * and the width of the bare soil between rows all derive from the same depth
 * value, so they agree with each other. Perspective drawn by eye disagrees with
 * itself somewhere, and that is what reads as amateur even when nobody can
 * point at the place.
 *
 * *Haze.* Distance is not smaller, it is paler. Every plant, furrow and tree is
 * mixed toward one pale horizon colour in proportion to how far off it is, and
 * a soft band sits across the horizon so that ground and sky dissolve into each
 * other instead of meeting at a line.
 *
 * # Why it is drawn rather than photographed
 *
 * The supplied artwork is 600x400 with type burned into the pixels, which is a
 * 3x upscale across a modern display and turns the captions to mush. This is
 * exact at any size, weighs nothing to download, and takes its greens from the
 * palette rather than from whatever a JPEG compressed them to.
 *
 * # Why the field is laid out at module load
 *
 * Two reasons, and they point the same way. The scatter has to come out
 * identical on the server and in the browser or React discards the markup it
 * was sent and redraws the whole field during hydration, so the generator is a
 * fixed-seed LCG — "random" here means "arbitrary, but the same every time".
 * And the field never changes, so laying it out once when the module loads is
 * both cheaper than doing it per render and the only way to write it: a
 * generator that advances a seed is a mutation, which is not something a render
 * function is allowed to do.
 */
const W = 1200;
const HT = 1000;
// Low in the frame on purpose. The four feature captions sit over the upper
// half of this panel, and a treeline drawn at the middle puts crowns straight
// through the words.
const HORIZON = 545;
const VX = 655; // vanishing point, right of centre so the rows sweep left
const SPREAD = 118; // gap between rows at the viewer's feet
const FURROW = 21; // half-width of bare soil between rows, likewise
const PLANT = 7; // a plant is 10 units tall before this multiplies it

let seed = 20260901;
const rnd = () => {
  seed = (seed * 1664525 + 1013904223) % 4294967296;
  return seed / 4294967296;
};
const jit = (n: number) => (rnd() - 0.5) * 2 * n;

// One projection for the whole picture. v is 0 at the horizon and 1 at the
// viewer's feet; ground distance goes as 1/v, so every size scales with v.
const yOf = (v: number) => HORIZON + (HT - HORIZON) * v;
const xOf = (u: number, v: number) => VX + u * SPREAD * v;

const mix = (a: number[], b: number[], t: number) =>
  `rgb(${a.map((c, i) => Math.round(c + (b[i] - c) * t)).join(" ")})`;

const LEAF = [46, 108, 36];
const PALE = [150, 178, 140]; // what everything turns into with distance
const hazeAt = (v: number) => Math.pow(1 - Math.min(v, 1), 1.9);

// Enough rows that the crop still reaches both edges of the frame where it
// meets the sky. Most of them fall off screen long before they reach the
// viewer, and are dropped below.
const ROWS = Array.from({ length: 43 }, (_, i) => i - 21);

// Plants down a row, spaced geometrically: close together near the viewer,
// further apart with distance, so the gap between them stays a constant
// fraction of their height. Even spacing on the ground is the honest version
// and looks worse — it piles a third of the field into an unreadable smear
// along the horizon and charges markup for every plant in it.
//
// The step is tuned so that consecutive plants are closer together than they
// are tall, at every depth. That is the difference between a crop and some
// weeds scattered on a lawn.
const DEPTHS: number[] = [];
for (let g = 0.75, step = 0.058; 1 / g >= 0.035; g += step, step *= 1.12) {
  DEPTHS.push(1 / g);
}
DEPTHS.reverse(); // far first, so nearer plants overlap them

// The last stretch to the skyline is drawn as strips rather than plants. Down
// there a row is twelve pixels from its neighbour, so covering the frame takes
// a hundred rows and returns a texture no one can resolve. One scalloped band
// per depth gives the same crop-tops-receding impression for one path instead
// of a hundred — and without it the field stops short and leaves a bare green
// meadow between the crop and the trees.
const NEAR = DEPTHS.filter((v) => v >= 0.25);
const FAR = DEPTHS.filter((v) => v < 0.25);

const V_FAR = NEAR[0];
const V_NEAR = 1.3;

const STRIPS = FAR.map((v, i) => {
  const y = yOf(v);
  // Down to where the next band up starts, so the two meet exactly. Ending at
  // a fixed depth instead left every band's lower edge showing through the one
  // in front as a horizontal stripe.
  const bottom = yOf(FAR[i + 1] ?? NEAR[0]);
  const bump = PLANT * v * 2.2;
  const period = Math.max(SPREAD * v, 7);
  let d = `M-40 ${y.toFixed(1)}`;
  for (let x = -40; x < W + 40; x += period) {
    d += `q${(period / 2).toFixed(1)} ${(-bump * 1.7).toFixed(1)} ${period.toFixed(1)} 0`;
  }
  return {
    d: `${d}L${W + 40} ${bottom.toFixed(1)}L-40 ${bottom.toFixed(1)}Z`,
    fill: mix(LEAF, PALE, hazeAt(v) * 0.85),
  };
});

const FURROWS = ROWS.slice(0, -1).map((u) => {
  const c = u + 0.5;
  const pt = (v: number, side: number) =>
    `${(xOf(c, v) + side * FURROW * v).toFixed(1)} ${yOf(v).toFixed(1)}`;
  return `M${pt(V_FAR, -1)}L${pt(V_FAR, 1)}L${pt(V_NEAR, 1)}L${pt(V_NEAR, -1)}Z`;
});

const PLANTS = NEAR.flatMap((v) =>
  ROWS.map((u) => ({
    x: xOf(u, v) + jit(9) * v,
    y: yOf(v) + jit(4) * v,
    s: (PLANT * v * (0.88 + rnd() * 0.26)).toFixed(2),
    tilt: jit(6).toFixed(1),
    fill: mix(
      LEAF.map((c) => c + jit(11)),
      PALE,
      hazeAt(v),
    ),
    // Past this point a plant is under fifteen pixels tall and the leaves are
    // no longer resolvable, so it gets a silhouette instead of five paths.
    far: v < 0.3,
    // The furthest rows fade in over the scalloped bands behind them. Without
    // this the field starts at a hard line, because that is the depth where
    // the drawing switches from bands to plants.
    o: Math.min(1, 0.15 + (v - 0.25) * 4.25).toFixed(2),
  })),
).filter((p) => p.x > -80 && p.x < W + 80);

const CLOUDS = [
  [230, 96, 240, 20, 0.55],
  [640, 62, 175, 13, 0.42],
  [880, 148, 210, 16, 0.5],
  [180, 208, 150, 11, 0.36],
  [1010, 246, 160, 10, 0.3],
];

// The skyline, as a treeline rather than as trees.
//
// The previous version drew individual trees: a trunk with a lobed crown on
// top. At this distance and under this much haze that shape is a lollipop, and
// a row of lollipops is clip art no matter how carefully the crown is lumped.
// Distant vegetation is not a set of objects, it is an uneven dark edge where
// the land meets the sky, so that is what this is — one silhouette per band,
// undulating, with nothing in it a viewer can name. The farmhouse went the same
// way and for the same reason.
const treeline = (drop: number, height: number, step: number) => {
  const pts: [number, number][] = [];
  for (let x = -40; x <= W + 40; x += step * (0.6 + rnd() * 0.8)) {
    pts.push([x, HORIZON - drop - rnd() * height]);
  }
  let d = `M-40 ${HORIZON}L${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
  for (let i = 1; i < pts.length; i++) {
    const [px, py] = pts[i - 1];
    const [cx, cy] = pts[i];
    // Control point above both ends, so each span crests instead of sagging.
    d += `Q${((px + cx) / 2).toFixed(1)} ${(Math.min(py, cy) - height * 0.3).toFixed(1)} ${cx.toFixed(1)} ${cy.toFixed(1)}`;
  }
  const last = pts[pts.length - 1][0];
  return `${d}L${last.toFixed(1)} ${HORIZON}Z`;
};
const TREELINE_FAR = treeline(14, 16, 46);
const TREELINE_NEAR = treeline(4, 13, 32);

// The rows, continued past the point where drawing plants stops paying. This
// is the cue that says "planted field" rather than "meadow", and it is 200
// converging segments in a single path.
const FAR_ROWS = (() => {
  const v = 0.3;
  let d = "";
  for (let u = -110; u <= 110; u++) {
    if (u === 0) continue;
    d += `M${VX} ${HORIZON}L${xOf(u, v).toFixed(1)} ${yOf(v).toFixed(1)}`;
  }
  return d;
})();

export function FieldScene({ className }: { className?: string }) {
  return (
    <svg
      viewBox={`0 0 ${W} ${HT}`}
      preserveAspectRatio="xMidYMid slice"
      className={className}
      role="presentation">
      <defs>
        {/* One crop plant, standing on the origin and ten units tall, so that
            the caller's scale factor is its height. Broad leaves, not blades:
            thin strokes at this size read as weeds on a lawn rather than as a
            planted row. */}
        <g id="es-crop">
          <path d="M0 0C-1.7-4-1.3-7.4 0-10.9C1.3-7.4 1.7-4 0 0Z" />
          <path d="M0 0C-2.9-3.4-5.7-4.7-6.5-8.7C-3.7-7.2-1.7-3.8 0 0Z" />
          <path d="M0 0C2.9-3.4 5.7-4.7 6.5-8.7C3.7-7.2 1.7-3.8 0 0Z" />
          <path d="M0 0C-3.7-1.7-6.9-2.3-8.3-5.3C-5.3-4.7-2.3-2.5 0 0Z" />
          <path d="M0 0C3.7-1.7 6.9-2.3 8.3-5.3C5.3-4.7 2.3-2.5 0 0Z" />
        </g>
        {/* Distant plants get a silhouette. Under fifteen pixels the leaves are
            not resolvable, and three hundred plants at five paths each is
            geometry the browser lays out for nothing. Note the unbroken top:
            an outline that dips in the middle stops being a plant and becomes
            a small heart, which is what the last attempt drew. */}
        <path
          id="es-crop-far"
          d="M0 0C-6.2-1.6-8.2-5.2-7.4-9C-5.2-11-2.1-11.8 0-11.9C2.1-11.8 5.2-11 7.4-9C8.2-5.2 6.2-1.6 0 0Z"
      />

        <linearGradient id="es-sky" x1="0" y1="0" x2="0.15" y2="1">
          <stop offset="0%" stopColor="#7fb4d8" />
          <stop offset="24%" stopColor="#a9cbdf" />
          <stop offset="48%" stopColor="#d3dfdb" />
          <stop offset="72%" stopColor="#f2e2bd" />
          <stop offset="90%" stopColor="#fdeccb" />
          <stop offset="100%" stopColor="#fff4dc" />
        </linearGradient>

        <radialGradient id="es-sun" cx="0.5" cy="0.5" r="0.5">
          <stop offset="0%" stopColor="#fffdf4" />
          <stop offset="14%" stopColor="#fff6d8" stopOpacity="0.95" />
          <stop offset="40%" stopColor="#ffe9ae" stopOpacity="0.55" />
          <stop offset="72%" stopColor="#ffdc93" stopOpacity="0.2" />
          <stop offset="100%" stopColor="#ffd489" stopOpacity="0" />
        </radialGradient>

        <linearGradient id="es-ground" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#a3b593" />
          <stop offset="12%" stopColor="#8fac6f" />
          <stop offset="45%" stopColor="#57933c" />
          <stop offset="100%" stopColor="#33741f" />
        </linearGradient>

        {/* Bare earth between the rows. One gradient in user space serves every
            furrow: they all run the same way down the same picture, and it
            fades the soil out with distance for free. */}
        <linearGradient
          id="es-furrow"
          x1="0"
          y1={HORIZON}
          x2="0"
          y2={HT}
          gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#7a7c4e" stopOpacity="0" />
          {/* Still nothing at 32%, which is where the furrows start. A gradient
              already climbing by then puts a hard horizontal edge across the
              field at their top. */}
          <stop offset="32%" stopColor="#6f7049" stopOpacity="0" />
          <stop offset="60%" stopColor="#655f3d" stopOpacity="0.15" />
          <stop offset="100%" stopColor="#4c4630" stopOpacity="0.26" />
        </linearGradient>

        {/* A horizon does not exist as a line in real air. Kept narrow and low:
            the previous version was a 230-unit band at 85%, which is not haze,
            it is a glass of milk over the top third of the field. */}
        <linearGradient id="es-haze" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="#fff3d8" stopOpacity="0" />
          <stop offset="50%" stopColor="#fff0cf" stopOpacity="0.62" />
          <stop offset="70%" stopColor="#f4eedb" stopOpacity="0.34" />
          <stop offset="100%" stopColor="#eef0d8" stopOpacity="0" />
        </linearGradient>

        <linearGradient id="es-warm" x1="0.3" y1="0" x2="1" y2="0.55">
          <stop offset="0%" stopColor="#ffd07a" stopOpacity="0.2" />
          <stop offset="50%" stopColor="#ffd07a" stopOpacity="0.06" />
          <stop offset="100%" stopColor="#ffd07a" stopOpacity="0" />
        </linearGradient>

        <radialGradient id="es-vignette" cx="0.5" cy="0.4" r="0.8">
          <stop offset="62%" stopColor="#0c3210" stopOpacity="0" />
          <stop offset="100%" stopColor="#0c3210" stopOpacity="0.13" />
        </radialGradient>

        <filter id="es-blur" x="-25%" y="-40%" width="150%" height="180%">
          <feGaussianBlur stdDeviation="11" />
        </filter>
        <filter id="es-blur-s" x="-25%" y="-40%" width="150%" height="180%">
          <feGaussianBlur stdDeviation="4" />
        </filter>
      </defs>

      <rect width={W} height={HORIZON + 2} fill="url(#es-sky)" />

      <g fill="#ffffff" filter="url(#es-blur)">
        {CLOUDS.map(([cx, cy, rx, ry, o], i) => (
          <ellipse key={i} cx={cx} cy={cy} rx={rx} ry={ry} opacity={o} />
        ))}
      </g>

      <circle cx="520" cy={HORIZON - 76} r="285" fill="url(#es-sun)" />
      <circle
        cx="520"
        cy={HORIZON - 76}
        r="46"
        fill="#fffdf2"
        filter="url(#es-blur-s)"
    />

      {/* Hills, stepping from air-coloured to nearly the field's own green. */}
      <path
        d={
          `M0 ${HORIZON - 64}Q150 ${HORIZON - 98} 320 ${HORIZON - 72}` +
          `Q470 ${HORIZON - 50} 620 ${HORIZON - 88}` +
          `Q780 ${HORIZON - 126} 940 ${HORIZON - 82}` +
          `Q1080 ${HORIZON - 46} ${W} ${HORIZON - 68}V${HORIZON}H0Z`
        }
        fill="#9dbac6"
        opacity="0.62"
    />
      <path
        d={
          `M0 ${HORIZON - 32}Q180 ${HORIZON - 62} 360 ${HORIZON - 42}` +
          `Q540 ${HORIZON - 24} 720 ${HORIZON - 46}` +
          `Q920 ${HORIZON - 70} ${W} ${HORIZON - 38}V${HORIZON}H0Z`
        }
        fill="#86a486"
        opacity="0.72"
    />

      <g>
        <path d={TREELINE_FAR} fill={mix(LEAF, PALE, 0.8)} opacity="0.55" />
        <path d={TREELINE_NEAR} fill={mix(LEAF, PALE, 0.68)} opacity="0.62" />
      </g>

      <rect y={HORIZON} width={W} height={HT - HORIZON} fill="url(#es-ground)" />

      <g>
        {STRIPS.map((st, i) => (
          <path key={i} d={st.d} fill={st.fill} />
        ))}
      </g>

      <path
        d={FAR_ROWS}
        fill="none"
        stroke="#587c49"
        strokeWidth="1.3"
        opacity="0.3"
    />

      <g fill="url(#es-furrow)">
        {FURROWS.map((d, i) => (
          <path key={i} d={d} />
        ))}
      </g>

      <g>
        {PLANTS.map((p, i) => (
          <use
            key={i}
            href={p.far ? "#es-crop-far" : "#es-crop"}
            fill={p.fill}
            opacity={p.o}
            transform={`translate(${p.x.toFixed(1)} ${p.y.toFixed(1)}) rotate(${p.tilt}) scale(${p.s})`}
        />
        ))}
      </g>

      {/* Everything above is veiled where it meets the sky. */}
      <rect y={HORIZON - 120} width={W} height="150" fill="url(#es-haze)" />
      <rect width={W} height={HT} fill="url(#es-warm)" />
      <rect width={W} height={HT} fill="url(#es-vignette)" />
    </svg>
  );
}
