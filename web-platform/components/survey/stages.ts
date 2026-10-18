import type { TiltTarget } from "@/lib/sensors";
import { LINES, type LineKey } from "@/lib/voice";

/**
 * The stages of a guided survey.
 *
 * # Why three photographs and not one
 *
 * They answer different questions and an assessor needs all three. The wide
 * shot establishes that this is a field and what state it is in overall. The
 * close-up shows what the damage actually is — lodging, shattering, submersion
 * look nothing alike. The crop shot identifies the crop and its growth stage,
 * which is what makes a claimed sowing date checkable.
 *
 * One photograph of the middle distance answers none of them well.
 *
 * # Why the order
 *
 * Wide first, because it is taken from the edge where the farmer is already
 * standing, and it is the shot that fails if they have walked into the middle
 * of the plot first. Then in, to the damage, then closer still.
 */

export interface Stage {
  id: string;
  /** The capture kind the server records this as. */
  kind: string;
  titleMr: string;
  titleEn: string;
  /** One sentence of what to do, shown large. */
  instructionMr: string;
  instructionEn: string;
  /** The spoken line for this stage. */
  line: LineKey;
  /** Concrete things that make the photograph usable. */
  tips: { mr: string; en: string }[];
  tilt: TiltTarget;
  /** Whether the farmer may move past without taking it. */
  optional: boolean;
}

export const STAGES: Stage[] = [
  {
    id: "wide",
    kind: "wide_shot",
    titleMr: "पूर्ण शेत",
    titleEn: "The whole field",
    instructionMr: "मागे जा आणि पूर्ण शेत दिसेल असा फोटो घ्या.",
    instructionEn: "Step back so the whole field is in the picture.",
    line: "stageWide",
    tips: [
      { mr: "शेताच्या बांधावर उभे रहा", en: "Stand at the edge of the field" },
      { mr: "फोन सरळ धरा, आकाशाकडे नको", en: "Hold the phone upright, not at the sky" },
      { mr: "आजूबाजूचा भाग पण दिसू द्या", en: "Let the surroundings show too" },
    ],
    tilt: "upright",
    optional: false,
  },
  {
    id: "damage",
    kind: "damage_closeup",
    titleMr: "नुकसान",
    titleEn: "The damage",
    instructionMr: "नुकसान झालेल्या पिकाजवळ जा आणि जवळून फोटो घ्या.",
    instructionEn: "Go up to the damaged crop and photograph it closely.",
    line: "stageDamage",
    tips: [
      { mr: "सर्वात जास्त नुकसान झाले तिथे जा", en: "Go where the damage is worst" },
      { mr: "फोन खाली पिकाकडे धरा", en: "Point the phone down at the crop" },
      { mr: "पडलेली, तुटलेली रोपे दिसू द्या", en: "Show the flattened or broken plants" },
    ],
    tilt: "downward",
    optional: false,
  },
  {
    id: "crop",
    kind: "crop_closeup",
    titleMr: "पीक",
    titleEn: "The crop",
    instructionMr: "एक रोप स्पष्ट दिसेल असा जवळून फोटो घ्या.",
    instructionEn: "One plant, close enough to see it clearly.",
    line: "stageCrop",
    tips: [
      { mr: "एकच रोप फ्रेममध्ये भरू द्या", en: "Let one plant fill the frame" },
      { mr: "पाने आणि शेंगा दिसू द्या", en: "Show the leaves and the pods" },
      { mr: "सावली पडणार नाही याची काळजी घ्या", en: "Try not to cast a shadow on it" },
    ],
    tilt: "downward",
    optional: false,
  },
  {
    id: "extra",
    kind: "wide_shot",
    titleMr: "आणखी काही",
    titleEn: "Anything else",
    instructionMr: "आणखी काही दाखवायचे असल्यास फोटो घ्या.",
    instructionEn: "Photograph anything else you want to show.",
    line: "stageWide",
    tips: [
      { mr: "पाणी साचले असल्यास", en: "Standing water, if there is any" },
      { mr: "शेजारच्या शेताशी तुलना", en: "A comparison with the next field" },
    ],
    tilt: "any",
    optional: true,
  },
];

/** The spoken and written line for a stage. */
export function stageLine(stage: Stage): { mr: string; en: string } {
  return LINES[stage.line];
}
