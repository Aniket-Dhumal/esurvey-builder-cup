

# --- payload bounds ----------------------------------------------------------
#
# This service decodes base64 into memory inside a one-gigabyte container. The
# API that calls it caps a capture at 8 MiB, but that is a fact about the
# current deployment rather than a property of this code, and the decoding
# happens here.

def _image(payload_chars: int) -> dict:
    return {
        "kind": "field_photo",
        "data_uri": "data:image/png;base64," + ("A" * payload_chars),
    }


def test_an_ordinary_photograph_is_accepted():
    # About 8 MiB of image, base64-expanded. The bound must not refuse the
    # thing it exists to make room for.
    from esurvey_ai.schemas import Image

    Image.model_validate(_image(11 * 1024 * 1024))


def test_an_oversized_data_uri_is_refused():
    import pytest
    from pydantic import ValidationError

    from esurvey_ai.schemas import MAX_DATA_URI_CHARS, Image

    with pytest.raises(ValidationError):
        Image.model_validate(_image(MAX_DATA_URI_CHARS + 1))


def test_too_many_images_are_refused():
    # A thousand small images cost as much as one enormous one.
    import pytest
    from pydantic import BaseModel, Field, ValidationError

    from esurvey_ai.schemas import MAX_IMAGES, Image

    class Carrier(BaseModel):
        images: list[Image] = Field(default_factory=list, max_length=MAX_IMAGES)

    Carrier.model_validate({"images": [_image(10)] * MAX_IMAGES})

    with pytest.raises(ValidationError):
        Carrier.model_validate({"images": [_image(10)] * (MAX_IMAGES + 1)})


def test_the_bound_is_refused_before_anything_is_decoded():
    # Pydantic validates the field before decode_image ever sees it, so an
    # oversized payload never reaches base64.b64decode. That ordering is the
    # whole protection: a bound applied after decoding would have already
    # allocated the memory it was meant to prevent.
    import pytest
    from pydantic import ValidationError

    from esurvey_ai.schemas import MAX_DATA_URI_CHARS, Image

    with pytest.raises(ValidationError):
        Image.model_validate(_image(MAX_DATA_URI_CHARS * 2))


# --- who may call this service ----------------------------------------------

def test_assessment_is_refused_without_the_token(monkeypatch):
    # This service holds the Gemini credential and is handed photographs of a
    # named person's land. Until a security review asked, it accepted work from
    # anything that could reach it.
    from esurvey_ai.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_ai.main import app

    with TestClient(app) as c:
        assert c.post("/api/v1/assess", json={}).status_code == 401


def test_the_right_token_is_let_through(monkeypatch):
    from esurvey_ai.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_ai.main import app

    with TestClient(app) as c:
        r = c.post(
            "/api/v1/assess",
            json={},
            headers={"Authorization": "Bearer a-shared-token"},
        )
        # 422 for the empty body is the right answer; 401 would not be.
        assert r.status_code != 401


def test_health_is_never_gated(monkeypatch):
    from esurvey_ai.config import settings

    monkeypatch.setattr(settings, "caller_token", "a-shared-token")

    from starlette.testclient import TestClient

    from esurvey_ai.main import app

    with TestClient(app) as c:
        assert c.get("/healthz").status_code == 200


def test_an_unset_token_leaves_the_check_off(monkeypatch):
    from esurvey_ai.config import settings

    monkeypatch.setattr(settings, "caller_token", "")

    from starlette.testclient import TestClient

    from esurvey_ai.main import app

    with TestClient(app) as c:
        assert c.post("/api/v1/assess", json={}).status_code != 401
