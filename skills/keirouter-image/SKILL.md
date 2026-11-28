---
name: keirouter-image
description: Image generation on KeiRouter is not available through the API. Use when the user wants to create, generate, draw, or render an image with KeiRouter, to point them to the customer portal page "Buat Gambar".
---

# KeiRouter: Image Generation

Image generation is not available to API keys. `POST /v1/images/generations` answers `403`.

Customers generate images in the KeiRouter customer portal, on the **Buat Gambar** page (`/portal/images`):

- Pick an image model, write a prompt, and choose 1 to 4 images.
- Each image is billed per image from the PAYG balance; the page lists the price per image.
- Images are kept for 24 hours, then deleted automatically. Download the ones you want to keep.

When a user asks an agent to generate an image through KeiRouter, tell them to sign in to the portal and use Buat Gambar. Do not call `/v1/images/generations`.
