# Signing XFA forms

This is what it takes to sign an XFA form with a smart card (a CAC or PIV card) so that Acrobat and Reader show the signature as valid. goxfa doesn't sign yet. This document is the plan.

## What a PDF signature is

A PDF signature is an incremental update to the file. It adds a signature dictionary, and a CMS (PKCS #7) signature inside that dictionary covers every byte of the file except the signature itself.

```
original PDF bytes                                  ← covered
incremental update:
  signature dictionary
    /Type /Sig
    /Filter /Adobe.PPKLite
    /SubFilter /adbe.pkcs7.detached  (or /ETSI.CAdES.detached)
    /ByteRange [0 a b c]             ← the two covered stretches
    /Contents <hex CMS ...>          ← the hole: everything else is covered
    /M (D:20261016194100Z)
  the field's /V pointing at it, its widget's appearance, /SigFlags 3
  xref and trailer                                  ← covered
```

The steps are:

1. Write the update with `/Contents` as a run of zeros big enough for the CMS, in hex. A three-certificate chain and a timestamp need about 12 KB of CMS, so 24 KB of hex. With embedded OCSP answers, allow 32 KB of CMS.
2. Fill in `/ByteRange` (from the start of the file to the `<`, and from after the `>` to the end) without changing any lengths.
3. Hash the two stretches with SHA-256.
4. Build the CMS SignedData. It is detached (no content inside it), with signed attributes for content type, message digest (the hash from step 3), signing time and, for CAdES, signing-certificate-v2. Hash the DER of the signed attributes and have the card sign that hash.
5. Write the CMS as hex into the zeros. Nothing else moves.

goxfa already writes incremental updates, encrypted or not (`appendUpdate` in pdfio.go). Signing adds the reserved hole, the byte range, and the CMS. With encryption, the `/Contents` string is never encrypted. Everything else in the update is encrypted as usual.

## What's different about XFA

**The signature field exists twice.** An XFA form describes its signature field in the template (`<field><ui><signature>`), and the PDF keeps a matching AcroForm field named by its SOM path, such as `form1[0].Page2[0].Signature[0]`. The PDF signature goes in the AcroForm field. Acrobat ties it to the XFA field by that name. In a dynamic form the AcroForm field often has no widget on a page, because the pages don't exist in the PDF. The signature still validates, and Acrobat draws its appearance in the XFA field's box.

**The data must be saved in the same revision, or earlier.** The signature covers the datasets stream as it stands in the signed revision. goxfa should write the values and the signature in one update, or write the values first and then sign. Anything saved after the signature shows in Acrobat as "changed after signing". Acrobat counts form filling after a signature as allowed or not according to the permissions below.

**The template says what signing locks and which certificates it takes.** An XFA signature field can carry:

- `<manifest>`: the fields this signature covers. After signing, Acrobat makes them read-only. The PDF equivalent is a `/Lock` dictionary on the signature field (`/Type /SigFieldLock /Action /Include /Fields [...]`) and a FieldMDP transform in the signature's `/Reference`. goxfa should write both, so the locks hold in Acrobat whatever it reads.
- `<filter>`: the certificates allowed (issuers, policy OIDs, key usage), digest methods, the subfilter, and whether a reason is required. goxfa should refuse a certificate the filter refuses, with a plain message, before asking for the PIN.
- `<mdp permissions="1|2|3">`: the first signature can certify the document. 1 allows no changes, 2 allows form filling and signing, and 3 also allows comments.

**The form packet's checksum.** Acrobat stores a checksum of the form state in the `form` packet. When the checksum doesn't match, Acrobat merges the data again on open and runs the initialize and calculate scripts. That changes nothing on disk, so a signature stays valid. But a calculated field can show a different value than the one that was signed. goxfa doesn't run scripts, so a form whose calculated fields matter should be checked in Acrobat before anyone relies on it.

**Reader usage rights.** Forms enabled for Reader carry a usage-rights signature (`/Perms /UR3`). An incremental update keeps it intact, which is why goxfa never rewrites the file. Acrobat checks that the changes after a UR3 signature are ones it allows. Filling fields and adding signatures are allowed.

## Reaching the card without cgo

The card's private key never leaves the card. The program sends a hash and gets a signature back. In Go, a `crypto.Signer` does exactly this, so the CMS code needs nothing more than a signer and the certificate chain.

| Platform | How | Notes |
|---|---|---|
| Windows | CNG through `ncrypt.dll` and the certificate store through `crypt32.dll`, called with `golang.org/x/sys/windows` and `syscall.NewLazyDLL`. No cgo. | Open the `MY` store, find the certificate, get its key with `CryptAcquireCertificatePrivateKey` (it comes back as an NCrypt key for smart cards), then sign with `NCryptSignHash` using PKCS #1 v1.5 padding and SHA-256. Windows shows its own PIN prompt through the card's minidriver. Most CAC users already have the middleware this needs. |
| macOS | The Keychain and CryptoTokenKit through the Security framework. | Needs cgo or `purego` with Objective-C calls. Alternatively, use PKCS #11 through OpenSC. |
| Linux | PKCS #11 through OpenSC (`opensc-pkcs11.so`), loaded with `purego`. No cgo. | The program asks for the PIN itself and passes it to `C_Login`. |

The card has more than one certificate. A CAC carries a PIV Authentication certificate, a Digital Signature certificate and an encryption certificate. Signing uses the Digital Signature certificate. Choose the one whose key usage includes digitalSignature and nonRepudiation, and whose extended key usage doesn't limit it to client authentication. Cards issued in recent years may hold ECC keys (P-256 or P-384) rather than RSA 2048. The CMS code should handle both, using ECDSA with SHA-256 or SHA-384.

The certificate's common name usually ends in the holder's ID number (`LAST.FIRST.MIDDLE.1234567890`). The signature's appearance can show the name without the number. The certificate itself is embedded in the signature as it is.

## Trust and long-term validation

**Trust.** Acrobat trusts a signature when its chain ends in a root that Acrobat trusts. Government roots are often not in Adobe's own trust list. Organizations either import their roots into Acrobat or turn on "Use Windows certificate store for validating signatures" in Acrobat's signature preferences. goxfa can't change what a reader trusts. It can embed the full chain up to the root, so that any reader with the root installed can validate.

**Revocation.** A reader checks whether the certificate was revoked, through OCSP or a CRL. If the signature is to stay valid after the certificate expires or the reader is offline, embed the answers at signing time. There are two ways:

- Adobe's `adbe-revocationInfoArchival` signed attribute (OID 1.2.840.113583.1.1.8), holding the OCSP responses and CRLs. Acrobat reads it. It has to be fetched before signing, because it is inside the signed attributes.
- A Document Security Store (PAdES-LTV, PDF 2.0): a `/DSS` dictionary in a later incremental update, with `/Certs`, `/OCSPs` and `/CRLs`. It can be added after signing.

CRLs for large PKIs can be tens of megabytes. Prefer OCSP.

**Time.** Without a timestamp, the signing time is the computer's clock, and readers say so. An RFC 3161 timestamp from a timestamp authority goes in the CMS as an unsigned attribute (`id-aa-timeStampToken`, 1.2.840.113549.1.9.16.2.14). It covers the signature value. Use one where an authority is reachable. Otherwise leave it out.

## What goxfa would add

```go
type SignOptions struct {
	Field    string            // the signature field's SOM path
	Signer   crypto.Signer     // the card's key
	Chain    []*x509.Certificate // signing certificate first
	Reason, Location string
	OCSP     [][]byte          // optional revocation answers to embed
	TSA      func(digest []byte) ([]byte, error) // optional RFC 3161 timestamp
}

func (f *Form) Sign(o SignOptions) ([]byte, error)
```

`Sign` would save any pending values first. Then it would check the field's filter, lock its manifest, reserve the hole, hash the byte ranges, build the CMS around the signer, and fill the hole. Signers live in their own packages (`sign/windows`, `sign/pkcs11`), so programs that don't sign don't link them.

## How it would be checked

- The signed bytes outside `/Contents` match the unsigned update exactly, and `/ByteRange` covers the whole file except the hole.
- The CMS verifies with Go's own code, `openssl cms -verify`, and pdfsig (poppler), and Acrobat shows the signature as valid. Acrobat is the check that matters.
- Changing one byte anywhere in the covered stretches breaks the signature.
- Encrypted forms: the signature dictionary's `/Contents` stays clear, and the rest of the update decrypts.
- Card tests run only where a card is present. Software keys, generated in the test, cover the rest.
