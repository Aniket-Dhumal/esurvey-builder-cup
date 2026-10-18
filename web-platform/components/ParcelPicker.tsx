"use client";

import { useEffect, useState } from "react";

import { api, ApiError } from "@/lib/api";
import type { CropSown, FarmerRecord, Parcel } from "@/lib/types";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

/**
 * Choosing one of a farmer's parcels.
 *
 * # Why this is a component and not a select on each page
 *
 * Because the same choice is made in two places -- requesting an assessment
 * and filing a claim -- and it was written twice: a select on one page, and
 * three text boxes on the other where an officer typed the survey number, the
 * farm id and the crop by hand. A typed farm id is a claim against whatever
 * land that id turns out to name.
 *
 * The rules that belong to this choice live here once: a parcel with no
 * boundary is listed but cannot be picked, and it says why rather than
 * vanishing; the crop and the owner's extent come from the registry rather
 * than from somebody's memory.
 */
export interface ParcelChoice {
  parcel: Parcel;
  /** The registry's crop for this parcel, where it carries one. */
  crop?: CropSown;
}

export function ParcelPicker({
  id,
  farmerId,
  value,
  onChange,
  /** Parcels with no boundary can be chosen anyway (a claim may name one; an
   *  assessment cannot run on one). */
  allowUnmapped = false,
  disabled,
}: {
  id?: string;
  farmerId: string;
  value: string;
  onChange: (choice: ParcelChoice | null) => void;
  allowUnmapped?: boolean;
  disabled?: boolean;
}) {
  // One answer, stamped with whose it is. Keeping the farmer id beside the
  // record is what lets the last farmer's land be ignored rather than cleared:
  // clearing is a second render, and for one frame the officer sees a parcel
  // list that belongs to somebody else.
  const [loaded, setLoaded] = useState<{
    farmerId: string;
    record: FarmerRecord | null;
    error: ApiError | null;
  } | null>(null);

  const answer = loaded?.farmerId === farmerId ? loaded : null;
  const record = answer?.record ?? null;
  const error = answer?.error ?? null;
  // The gap between the chosen farmer and the answered one is a fact, not a
  // flag somebody has to remember to lower.
  const loading = !!farmerId && !answer;

  useEffect(() => {
    if (!farmerId) return;
    let live = true;
    api
      .getFarmer(farmerId)
      .then((r) => live && setLoaded({ farmerId, record: r, error: null }))
      .catch(
        (e) =>
          live &&
          setLoaded({ farmerId, record: null, error: e as ApiError }),
      );
    return () => {
      live = false;
    };
  }, [farmerId]);

  const parcels = record?.land_data ?? [];
  const cropFor = (farmID: string) =>
    record?.crop_sown_data?.find((c) => c.farm_id === farmID);

  const choose = (farmID: string) => {
    const parcel = parcels.find((p) => p.farm_id === farmID);
    onChange(parcel ? { parcel, crop: cropFor(farmID) } : null);
  };

  return (
    <>
      <Select
        value={value}
        onValueChange={choose}
        disabled={disabled || !farmerId || loading || parcels.length === 0}
      >
        <SelectTrigger id={id} className="w-full">
          <SelectValue
            placeholder={
              !farmerId
                ? "Choose a farmer first…"
                : loading
                  ? "Loading…"
                  : parcels.length === 0
                    ? "This farmer has no parcels in the registry"
                    : "Select a parcel…"
            }
          />
        </SelectTrigger>
        <SelectContent>
          {parcels.map((p) => {
            const crop = cropFor(p.farm_id);
            const mapped = !!p.plot_geometry;
            return (
              <SelectItem
                key={p.farm_id}
                value={p.farm_id}
                disabled={!mapped && !allowUnmapped}
              >
                Survey {p.survey_number}
                {p.sub_survey_number && p.sub_survey_number !== "*"
                  ? `/${p.sub_survey_number}`
                  : ""}
                {crop?.crop_name ? ` · ${crop.crop_name}` : ""}
                {p.owner_extent ? ` · ${p.owner_extent} ${p.area_unit}` : ""}
                {mapped ? "" : " — no boundary"}
              </SelectItem>
            );
          })}
        </SelectContent>
      </Select>

      {error && (
        <p className="text-xs text-destructive">
          {error.code === "FORBIDDEN"
            ? "This farmer is outside your area."
            : error.message}
        </p>
      )}
    </>
  );
}
