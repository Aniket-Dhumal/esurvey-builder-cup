/**
 * An officer's login is their email address.
 *
 * The same rule as the server's identity.NormalizeEmail, checked here so a
 * visible mistake is pointed out before a round trip. The server checks again
 * and is the authority; this only saves the officer a wait.
 *
 * Returns the normalised address, or a sentence saying what is wrong with it.
 */

const LOCAL = /^[a-z0-9!#$%&'*+/=?^_{|}~-]+(\.[a-z0-9!#$%&'*+/=?^_{|}~-]+)*$/;
const LABEL = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
const TLD = /^[a-z]{2,63}$/;

const RESERVED_TLDS = new Set([
  "local", "localhost", "test", "example", "invalid", "internal",
  "lan", "home", "corp", "intranet", "private", "localdomain",
]);
const RESERVED_DOMAINS = new Set(["example.com", "example.net", "example.org"]);

export function checkEmail(raw: string): { email: string } | { error: string } {
  const e = raw.trim().toLowerCase();
  const fail = (error: string) => ({ error });

  if (e === "") return fail("Enter your email address.");
  if (e.length > 254) return fail("That is longer than an email address can be.");
  if (/[\s"(),:;<>[\]\\]/.test(e)) {
    return fail("An email address cannot contain spaces or those characters.");
  }
  if (e.split("@").length !== 2) return fail("An email address has exactly one @.");

  const [local, domain] = e.split("@");
  if (!local) return fail("The part before the @ is missing.");
  if (local.length > 64) return fail("The part before the @ is too long.");
  if (!LOCAL.test(local)) {
    return fail("The part before the @ is not valid — check for a leading, trailing or double dot.");
  }
  if (!domain) return fail("The domain after the @ is missing.");

  const labels = domain.split(".");
  if (labels.length < 2) return fail("The domain needs a dot, as in nic.in or gov.in.");
  if (!labels.every((l) => LABEL.test(l))) {
    return fail("The domain is not valid — check for a double dot or a stray hyphen.");
  }
  const tld = labels[labels.length - 1];
  if (!TLD.test(tld)) return fail("The domain must end in a real suffix such as .in or .com.");
  if (RESERVED_TLDS.has(tld)) return fail(`.${tld} addresses cannot receive mail.`);
  if (RESERVED_DOMAINS.has(domain)) return fail(`${domain} is reserved and cannot receive mail.`);

  return { email: e };
}
