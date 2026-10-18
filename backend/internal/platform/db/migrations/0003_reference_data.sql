-- Reference jurisdictions for the demo footprint.
--
-- These are the real LGD codes carried by the mock registry record: state 27,
-- district 496, sub-district 4246, village 562111. Only the branch the mock
-- parcel sits in is seeded -- the full LGD master for Maharashtra is ~44,000
-- villages and belongs in a loader, not a migration.
--
-- Names are the ones we can vouch for. Where the LGD name master has not been
-- loaded the code stands in for the name rather than a guess being recorded
-- as fact.

INSERT INTO jurisdictions (id, level, code, name, parent_id) VALUES
    ('11111111-0000-4000-8000-000000000027', 'state', '27', 'Maharashtra', NULL);

INSERT INTO jurisdictions (id, level, code, name, parent_id) VALUES
    ('11111111-0000-4000-8000-000000000496', 'district', '496', 'Solapur',
     '11111111-0000-4000-8000-000000000027');

INSERT INTO jurisdictions (id, level, code, name, parent_id) VALUES
    ('11111111-0000-4000-8000-000000004246', 'sub_district', '4246',
     'Sub-district 4246', '11111111-0000-4000-8000-000000000496');

-- A circle has no LGD code, so it carries a locally assigned one prefixed to
-- keep it from ever colliding with a real code.
INSERT INTO jurisdictions (id, level, code, name, parent_id) VALUES
    ('11111111-0000-4000-8000-0000c0004246', 'circle', 'C-4246-01',
     'Malegaon Circle', '11111111-0000-4000-8000-000000004246');

INSERT INTO jurisdictions (id, level, code, name, parent_id) VALUES
    ('11111111-0000-4000-8000-000000562111', 'village', '562111', 'Malegaon',
     '11111111-0000-4000-8000-0000c0004246');
