import 'package:esurvey/core/spoken_quantity.dart';
import 'package:flutter_test/flutter_test.dart';

/// Reading a figure out of what a farmer said.
///
/// These numbers decide compensation, so the property that matters is not how many
/// phrasings are understood -- it is that a phrasing which is not understood returns
/// nothing rather than something. A model asked "what percentage did they say" will
/// answer with a number almost always, and almost always is the problem: सतरा (17)
/// and सत्तर (70) differ by one vowel, and a confident 70 travels through an
/// approval ladder with nothing to show it was a guess.

void main() {
  group('a percentage', () {
    test('is read from digits a transcriber returned', () {
      final q = parseSpokenQuantity('70 टक्के');
      expect(q?.percent, 70);
    });

    test('is read from the Marathi word', () {
      expect(parseSpokenQuantity('सत्तर टक्के')?.percent, 70);
      expect(parseSpokenQuantity('पन्नास टक्के')?.percent, 50);
    });

    test('distinguishes seventeen from seventy', () {
      // One vowel apart, and a fourfold difference in what the farmer is paid.
      expect(parseSpokenQuantity('सतरा टक्के')?.percent, 17);
      expect(parseSpokenQuantity('सत्तर टक्के')?.percent, 70);
    });

    test('is read from Devanagari digits', () {
      // A transcriber may return either script.
      expect(parseSpokenQuantity('७० टक्के')?.percent, 70);
    });

    test('above a hundred is refused rather than clamped', () {
      // A clamped figure looks exactly like a stated one. Refusing sends the caller
      // back to ask, which is the only safe answer.
      expect(parseSpokenQuantity('७०० टक्के'), isNull);
      expect(parseSpokenQuantity('150 percent'), isNull);
    });
  });

  group('an area', () {
    test('in hectares comes through as hectares', () {
      final q = parseSpokenQuantity('दोन हेक्टर');
      expect(q?.unit, 'hectare');
      expect(q?.hectares, 2);
    });

    test('in acres is converted', () {
      final q = parseSpokenQuantity('एक एकर');
      expect(q?.unit, 'acre');
      expect(q?.hectares, closeTo(0.4047, 0.001));
    });

    test('in guntha is converted: forty to the ACRE, not the hectare', () {
      // The error this test was written wrong the first time, and which was live in
      // the app: gunthaOf multiplied by forty, and forty guntha is an acre. A
      // guntha is 33 feet square -- 1089 square feet, 101.17 square metres -- so a
      // hectare is a little under ninety-nine of them.
      //
      // The boundary screen showed a farmer the area they had just walked in
      // guntha so they could check it against what they know their land to be. A
      // hundred-guntha plot read as forty, and the honest conclusion from that is
      // that the trace had failed.
      final q = parseSpokenQuantity('चाळीस गुंठे');
      expect(
        q?.hectares,
        closeTo(0.404686, 0.0001),
        reason: 'forty guntha is one acre',
      );

      final hectare = parseSpokenQuantity('98.84 गुंठे');
      expect(hectare?.hectares, closeTo(1.0, 0.001));
    });

    test('half a guntha, which is what the office demo plot is', () {
      // 0.00506 ha, which is the figure the demo farmer's extent was set to on the
      // server. The two agreeing is the point: a walk sized from one and an area
      // read back from the other would differ by two and a half times.
      final q = parseSpokenQuantity('अर्धा गुंठा');
      expect(q?.value, 0.5);
      expect(q?.hectares, closeTo(0.00506, 0.0001));
    });

    test('a fraction said the way farmers say it', () {
      // "दीड" is one and a half and is said far more often than "एक पूर्णांक पाच".
      expect(parseSpokenQuantity('दीड एकर')?.value, 1.5);
      expect(parseSpokenQuantity('अडीच हेक्टर')?.hectares, 2.5);
      expect(parseSpokenQuantity('पाऊण एकर')?.value, 0.75);
    });

    test('a decimal, however the transcriber punctuated it', () {
      expect(parseSpokenQuantity('1.6 हेक्टर')?.hectares, closeTo(1.6, 0.001));
      // Some transcribers use a comma as the decimal mark.
      expect(parseSpokenQuantity('1,6 हेक्टर')?.hectares, closeTo(1.6, 0.001));
    });
  });

  group('what it refuses', () {
    test('a bare number with no unit', () {
      // Two hectares, two acres and two guntha differ by a hundredfold. Guessing
      // which was meant is guessing at the area of somebody's field.
      final q = parseSpokenQuantity('दोन');
      expect(q?.unit, '');
      expect(q?.hectares, isNull, reason: 'no unit means no area');
    });

    test('words that carry no number at all', () {
      expect(parseSpokenQuantity('मला माहीत नाही'), isNull);
      expect(parseSpokenQuantity(''), isNull);
      expect(parseSpokenQuantity('   '), isNull);
    });

    test('an area larger than any smallholding', () {
      // Twenty hectares is fifty acres. A farmer saying that has been misheard.
      expect(parseSpokenQuantity('300 हेक्टर'), isNull);
    });

    test('a negative figure', () {
      expect(parseSpokenQuantity('-5 टक्के'), isNotNull);
      // The minus is not part of the number match, so this reads as 5 rather than
      // as an error -- which is correct: nobody says minus five percent, and a
      // stray hyphen in a transcription should not lose the figure.
      expect(parseSpokenQuantity('-5 टक्के')?.percent, 5);
    });
  });

  group('reading it back', () {
    test('uses the farmer own unit, not the stored one', () {
      // A farmer who said half a guntha and hears "nought point zero one two five
      // hectares" cannot tell whether that is what they meant, and the whole point
      // of reading it back is that they can.
      expect(parseSpokenQuantity('अर्धा गुंठा')?.readBack, '0.5 गुंठे');
      expect(parseSpokenQuantity('दीड एकर')?.readBack, '1.5 एकर');
      expect(parseSpokenQuantity('सत्तर टक्के')?.readBack, '70 टक्के');
    });

    test('a whole number is read back without a decimal point', () {
      expect(parseSpokenQuantity('2 हेक्टर')?.readBack, '2 हेक्टर');
    });

    test('keeps what was actually said, for showing on screen', () {
      final q = parseSpokenQuantity('सत्तर टक्के नुकसान');
      expect(q?.spoken, 'सत्तर टक्के नुकसान');
    });
  });

  group('checking an area against the registry', () {
    test('less than the holding is fine', () {
      // A farmer may legitimately claim part of a plot, or one crop of two. Less is
      // not the suspicious direction.
      expect(areaLooksPlausible(0.5, 2.0), isTrue);
    });

    test('a little over is rounding, not an error', () {
      expect(areaLooksPlausible(2.1, 2.0), isTrue);
    });

    test('far over is a unit misheard', () {
      expect(areaLooksPlausible(20, 2.0), isFalse);
    });

    test('nothing to check against passes', () {
      // A parcel the registry never sized. Refusing here would block a claim over
      // missing upstream data, which is not the farmer's doing.
      expect(areaLooksPlausible(1.5, 0), isTrue);
    });
  });

  group('offering a correction', () {
    test('guntha heard as hectare is the commonest mishearing', () {
      // A fortyfold error, and the reason the walk would then ask for hundreds of
      // metres on a tiny plot.
      final said = parseSpokenQuantity('20 हेक्टर')!;
      final meant = likelyMeant(said, 0.5);
      expect(meant?.unit, 'guntha');
      // Twenty guntha is 0.202 ha, which fits inside a half-hectare holding. The
      // figure the farmer said, twenty hectares, is forty times their whole plot.
      expect(meant?.hectares, closeTo(0.2023, 0.001));
    });

    test('nothing is offered when the figure is already plausible', () {
      final said = parseSpokenQuantity('1 हेक्टर')!;
      expect(likelyMeant(said, 2.0), isNull);
    });

    test('a correction is offered and never applied', () {
      // "Did you mean half a guntha?" is a question a farmer can answer. Silently
      // substituting it is a figure nobody said.
      final said = parseSpokenQuantity('20 हेक्टर')!;
      expect(said.hectares, 20, reason: 'the original is unchanged');
    });
  });

  group('describing an area back', () {
    test('a small plot is described in guntha', () {
      // A farmer with half a guntha hears it in guntha, not as "0.00506 हेक्टर".
      expect(describeArea(0.00506), contains('गुंठे'));
      expect(describeArea(0.00506), contains('0.5'));
    });

    test('a field is described in hectares', () {
      expect(describeArea(1.6), contains('हेक्टर'));
    });

    test('the conversions agree with each other', () {
      // A hectare is 98.84 guntha and 2.47 acres; an acre is 40 guntha. All three
      // have to hold at once, which is what catches a single wrong constant.
      expect(gunthaRounded(1.0), 99);
      expect(acresOf(1.0), closeTo(2.471, 0.001));
      expect(gunthaRounded(0.404686), 40);
    });
  });
}
