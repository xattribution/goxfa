# Signing XFA forms

This is what it takes to sign an XFA form with a smart card so that Acrobat and Reader show the signature as valid, and what goxfa does of it. The last sections say what's built, how it's checked, and what's left.

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

goxfa does exactly this (`Form.Sign` in sign.go, the CMS in internal/cms). With encryption, the `/Contents` string is never encrypted. Everything else in the update is encrypted as usual.

## What's different about XFA

**The signature field exists twice.** An XFA form describes its signature field in the template (`<field><ui><signature>`), and the PDF keeps a matching AcroForm field named by its SOM path, such as `form1[0].Page2[0].Signature[0]`. The PDF signature goes in the AcroForm field. Acrobat ties it to the XFA field by that name. In a dynamic form the AcroForm field often has no widget on a page, because the pages don't exist in the PDF. The signature still validates, and Acrobat draws its appearance in the XFA field's box.

**The data must be saved in the same revision, or earlier.** The signature covers the datasets stream as it stands in the signed revision. goxfa writes any values set since the form was opened and the signature in one update. Anything saved after the signature shows in Acrobat as "changed after signing". Acrobat counts form filling after a signature as allowed or not according to the permissions below.

**The template says what signing locks and which certificates it takes.** An XFA signature field can carry:

- `<manifest>`: the fields this signature covers. After signing, Acrobat makes them read-only. The PDF equivalent is a `/Lock` dictionary on the signature field (`/Type /SigFieldLock /Action /Include /Fields [...]`) and a FieldMDP transform in the signature's `/Reference`. goxfa enforces the manifest itself (a locked field refuses new values) but doesn't write either PDF entry yet.
- `<filter>`: the certificates allowed (issuers, policy OIDs, key usage), digest methods, the subfilter, and whether a reason is required. None of the forms tested carries one, so goxfa doesn't read it yet. It does refuse certificates that aren't for signing.
- `<mdp permissions="1|2|3">`: the first signature can certify the document. 1 allows no changes, 2 allows form filling and signing, and 3 also allows comments.

**The form packet's checksum.** Acrobat keeps the form's state beside its data in the `form` packet: instance managers, fields' override flags, and the page numbers below. A `checksum` attribute on the packet's root says the state still goes with the data. When the checksum matches, Acrobat uses the saved state. When it doesn't, Acrobat merges the data again on open and runs the initialize and calculate scripts.

The checksum is SHA-1 over a re-serialization of the template packet, then the datasets packet, written in base64. This is PDFium's `CXFA_ChecksumContext` (xfa_checksum.cpp, called from fpdfsave.cpp), and goxfa's `formChecksum` (checksum.go) follows it:

- start tag: `<name`, then ` attr="value"` for each attribute (value as written), then `>`
- empty element: `<name …></name>`
- end tag: `</name>`
- text: as written, entities not converted
- CDATA: `<![CDATA[…]]>`
- comment: `<!--…-->`
- processing instruction: `<?target data?>`

Text made only of bytes up to 0x20 is dropped when a start tag, comment or instruction follows it, and kept before an end tag. It reproduces the checksums Acrobat wrote on a real form and those of 40 of 43 published forms. The other three look published stale, the way a server that fills in a date without Acrobat leaves a form.

When `Save` and `Sign` write the datasets, they also write the form packet in the same revision, with its page numbers set and the checksum for the datasets they write (formstate.go). They do this only when `Form.WritesState` is true:

- the form has a form packet;
- its template runs no calculate script (other than the page numbers') and no initialize script.

goxfa runs no scripts, so for a form with those it leaves the checksum as it was, and Acrobat runs them when it opens the form. Either way nothing on disk changes after a signature, so the signature stays valid. On a form that calculates, a calculated field can show a different value than the one that was signed, so check such a form in Acrobat before anyone relies on it.

**Page numbers after a signature.** Many forms put "Page n of m" fields on their master pages. The fields are bound to nothing, and their layout:ready scripts fill them in (`this.rawValue = xfa.layout.page(this)`). Acrobat saves their values in the form packet, a pageArea element per page under the root subform's pageSet. When Acrobat doesn't use the saved state, or the state has no page numbers, it counts these fields as "Form Fields Filled In" after the signature. The bar across the top then says "unsigned changes after the last signature", though the signature is valid and nothing in the file changed.

A form goxfa signed showed this before goxfa kept the form state (Oct 2026). On a real form downloaded fresh from its publisher, signed with a test certificate (25 Oct 26), Acrobat's results were:

| Values | Checksum | Page numbers in the form packet | Acrobat |
|---|---|---|---|
| filled | stale (goxfa before) | no | listed as filled in |
| filled | current | yes | clean |
| none | current | yes | clean |
| filled | current | no | listed |
| filled | stale | yes | listed |

Both parts are needed. A form whose state goxfa doesn't keep (`WritesState` false) and that fills in its own page numbers (`Form.PageNumbered`) is still listed. An application can send those forms to Acrobat to sign, or warn before it signs one. Signing in Acrobat, or a later signature added in Acrobat, saves the state with a matching checksum. TestRealForms checks goxfa's master pages and page numbers against the ones Acrobat saved, and that the checksum is current after `Save`.

**Reader usage rights.** Forms enabled for Reader carry a usage-rights signature (`/Perms /UR3`). An incremental update keeps it intact, which is why goxfa never rewrites the file. Acrobat checks that the changes after a UR3 signature are ones it allows. Filling fields and adding signatures are allowed.

## Reaching the card without cgo

The card's private key never leaves the card. The program sends a hash and gets a signature back. In Go, a `crypto.Signer` does exactly this, so the CMS code needs nothing more than a signer and the certificate chain.

| Platform | How | Notes |
|---|---|---|
| Windows | CNG through `ncrypt.dll` and the certificate store through `crypt32.dll`, called with `golang.org/x/sys/windows` and `syscall.NewLazyDLL`. No cgo. | Open the `MY` store, find the certificate, get its key with `CryptAcquireCertificatePrivateKey` (it comes back as an NCrypt key for smart cards), then sign with `NCryptSignHash` using PKCS #1 v1.5 padding and SHA-256. Windows shows its own PIN prompt through the card's minidriver. Most smart card users already have the middleware this needs. |
| macOS | The Keychain and CryptoTokenKit through the Security framework. | Needs cgo or `purego` with Objective-C calls. Alternatively, use PKCS #11 through OpenSC. |
| Linux | PKCS #11 through OpenSC (`opensc-pkcs11.so`), loaded with `purego`. No cgo. | The program asks for the PIN itself and passes it to `C_Login`. |

A card usually holds more than one certificate: one for signing in, one for signing documents, and one for encryption. Signing uses the document-signing one. Choose the one whose key usage includes digitalSignature and nonRepudiation, and whose extended key usage doesn't limit it to client authentication. Cards issued in recent years may hold ECC keys (P-256 or P-384) rather than RSA 2048. The CMS code should handle both, using ECDSA with SHA-256 or SHA-384.

Some cards end the certificate's common name with the holder's ID number (`LAST.FIRST.MIDDLE.1234567890`). The signature's appearance can show the name without the number. The certificate itself is embedded in the signature as it is.

## Trust and long-term validation

**Trust.** Acrobat trusts a signature when its chain ends in a root that Acrobat trusts. Many organizations' roots are not in Adobe's own trust list. Organizations either import their roots into Acrobat or turn on "Use Windows certificate store for validating signatures" in Acrobat's signature preferences. goxfa can't change what a reader trusts. It can embed the full chain up to the root, so that any reader with the root installed can validate.

**Revocation.** A reader checks whether the certificate was revoked, through OCSP or a CRL. If the signature is to stay valid after the certificate expires or the reader is offline, embed the answers at signing time. There are two ways:

- Adobe's `adbe-revocationInfoArchival` signed attribute (OID 1.2.840.113583.1.1.8), holding the OCSP responses and CRLs. Acrobat reads it. It has to be fetched before signing, because it is inside the signed attributes.
- A Document Security Store (PAdES-LTV, PDF 2.0): a `/DSS` dictionary in a later incremental update, with `/Certs`, `/OCSPs` and `/CRLs`. It can be added after signing.

CRLs for large PKIs can be tens of megabytes. Prefer OCSP.

**Time.** Without a timestamp, the signing time is the computer's clock, and readers say so. An RFC 3161 timestamp from a timestamp authority goes in the CMS as an unsigned attribute (`id-aa-timeStampToken`, 1.2.840.113549.1.9.16.2.14). It covers the signature value. Use one where an authority is reachable. Otherwise leave it out.

## What goxfa does

```go
signed, err := form.Sign(xfa.SignOptions{
	Field:  "form1[0].Page1[0].Signature[0]",
	Signer: signer,           // any crypto.Signer: a card's key through winsign, or a software key
	Chain:  chain,            // signing certificate first, then its issuers
	Reason: "I agree",
	OCSP:   answers,          // optional: from xfa.FetchOCSP
	Timestamp: xfa.TimestampAuthority(url, nil), // optional
})
```

| Step | What happens |
|---|---|
| Check | The field is a signature field and isn't signed. The certificate is valid now and is for signing, and it belongs to the signer's key. |
| Field | A static form's existing AcroForm field gets the signature and an appearance ("Digitally signed by DOE, JANE A" and the time). A dynamic form gets a new AcroForm field named by the SOM path, one level per segment, with an invisible widget on the first page. |
| Appearance | The widget's picture, sized to the field's box. By default it's the two lines above. `Text` replaces the lines (an empty slice draws none), and `Graphic` adds a picture: on the left half with the text on the right, as Acrobat lays out a signature with a graphic, or across the whole box when there's no text. The graphic keeps its proportions and its transparency (a soft mask), and it may be at most 4096 pixels on a side. A program that wants a drawn name, an emblem or a handwriting font renders them into the graphic. |
| Update | One incremental update holds the new values (if any), the field, the AcroForm with `/SigFlags 3`, and the signature dictionary with its holes. Encrypted forms get their strings and streams encrypted, except `/Contents`. |
| Sign | SHA-256 over the byte ranges (SHA-384 for P-384 keys), then a detached CMS: content type, message digest, signing time, optionally signing-certificate-v2 (CAdES), Adobe's revocation archive with the OCSP answers, and an RFC 3161 timestamp on the signature value. |
| After | The form reopens from the signed bytes. `SignatureFields` reports the signer and time, the manifest's fields refuse new values (`ErrLocked`), and the layout shows the signature in the field. Later saves go after the signature. |

Programs that serve the live form can let people sign from the page: `HandlerOptions.Sign` gives the signer for a click on a signature field, and the signed PDF goes to `Save`.

**The Windows certificate store** is reached through package `winsign`, with no cgo. `winsign.SigningCertificates()` lists the certificates that have a private key and are for signing. On a smart card that leaves the document-signing certificate: the sign-in certificates (client authentication and smart card logon only) and the encryption certificate are left out. `Cert.Signer()` opens the key through CNG, and Windows asks for the PIN when the signature is made. The chain is built from what Windows has installed, and nothing is fetched. On a computer without the issuing CAs installed, `xfa.CompleteChain` fetches them from the certificate's issuer URL, so the signature carries the whole chain.

The command line does the same: `xfa certs` lists the certificates, `xfa sigs form.pdf` lists the signature fields, and `xfa sign [-cert N] [-reason ...] [-tsa URL] [-ocsp] form.pdf field out.pdf` signs, fetching missing issuers unless `-fetch-issuers=false`. Off Windows, `-key` and `-chain` take PEM files.

## How it's checked

- The tests sign with RSA and ECDSA (P-384) keys, as adbe.pkcs7.detached and CAdES. The byte range covers the whole file except the hole, and poppler's pdfsig reports "Signature is Valid" and "Total document signed".
- The CMS verifies with `openssl cms -verify`, and a changed byte fails it.
- With `GOXFA_FORMS` set, the tests fill and sign one signature field in each real form, encrypted and not, dynamic and static, with and without Reader usage rights. Each signed file passes qpdf's check and pdfsig, and pdf.js opens the signed dynamic forms.
- Timestamps and OCSP are tested against a fake authority and responder.

## What's left

- **Acrobat.** A real form signed by goxfa opens clean in Acrobat: no changes listed after the signature (see Page numbers after a signature, 25 Oct 26). What's left are forms that run initialize or calculate scripts, whose state goxfa doesn't keep (`WritesState` false). Acrobat still lists their page numbers as filled in after the signature.
- **Other XFA viewers.** Viewers that lay out XFA themselves, such as pdfcraft, draw the signed AcroForm field's own picture in the XFA box. So every widget goxfa signs carries an appearance sized to the field's box, even when it has no place on a PDF page.
- **The Windows signer on a real card.** It works: a smart card on Windows signed a real form through `winsign`, with the PIN prompt from Windows, and poppler reports the signature valid with the full certificate chain embedded. Only the card's signature certificate is offered; the authentication, card-authentication and encryption certificates are left out.
- **Locks in the PDF.** Manifests are enforced inside goxfa only. Writing `/Lock` and FieldMDP needs checking against what Acrobat writes for XFA forms.
- **Filters and certification.** The template's `<filter>` and `<mdp>` aren't read.
- **macOS and Linux cards.** PKCS #11 through OpenSC, loaded with purego, is the plan. Any `crypto.Signer` works in the meantime.
- **Long-term validation after signing.** A `/DSS` added in a later update isn't written. Embedding OCSP answers at signing time covers most needs.
